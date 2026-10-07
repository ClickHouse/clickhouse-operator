#!/usr/bin/env python3
"""
Pre-hook that provisions the Go toolchain + CI tools for the Operator CI jobs,
backed by an S3 cache so version bumps need no AMI rebuild.

What it provisions (into default, env-free locations so the job command needs no
extra env): the Go SDK (/usr/local/go), helm + kubebuilder + the go-installed
CLIs (controller-gen, kustomize, setup-envtest, golangci-lint, actionlint,
crd-schema-checker, crd-ref-docs) on PATH, a warm Go module + build cache
(/root/go, /root/.cache/go-build), the codespell pip wheel cache, and the
envtest K8s assets.

Versions are parsed from go.mod / Makefile at runtime (helm is pinned here), so
the cache key tracks the repo's own pins — bump a version in the Makefile and the
next run rebuilds + repopulates the cache automatically.

The whole tree is cached as one bundle (S3PathCache). The key is the version set,
NOT go.sum: dependency bumps are served incrementally from the warm module cache
rather than forcing a full rebuild on every dependabot PR.
"""
import glob
import os
import re
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, os.getcwd())  # ensure repo root is importable for ci.*
from ci.jobs.s3_cache import S3PathCache
from ci.settings.settings import AWS_REGION, CACHE_S3_PATH
from praktika.settings import Settings as PraktikaSettings

# Basename pattern for the per-arch go-env bundle distributed as a praktika
# artifact (see JobConfigs prep jobs). The consumer's `install` mode extracts
# whichever one its `requires` placed in the input dir.
ARTIFACT_GLOB = "go-env*.tar.zst"

HELM_VERSION = "v3.19.0"  # helm has no Makefile pin; bump here
ENVTEST_ASSETS_DIR = "/opt/ci-go/envtest"
GOBIN = "/usr/local/bin"
# HOME-independent cache locations. The consumer job runs under a different $HOME
# than this script's build (which uses /root), so caches under /root/go would be
# ignored and modules re-downloaded. We put them at fixed /opt paths and point the
# job's `go` at them with `go env -w` (writes a config file, so it survives the
# pre-hook -> command boundary regardless of $HOME).
GO_MODCACHE = "/opt/ci-go/gomodcache"
GO_BUILDCACHE = "/opt/ci-go/gocache"
GOPATH_DIR = "/opt/ci-go/gopath"

# go-installed CLIs: (binary, go package, Makefile version variable)
GO_TOOLS = [
    ("controller-gen", "sigs.k8s.io/controller-tools/cmd/controller-gen", "CONTROLLER_TOOLS_VERSION"),
    ("kustomize", "sigs.k8s.io/kustomize/kustomize/v5", "KUSTOMIZE_VERSION"),
    ("setup-envtest", "sigs.k8s.io/controller-runtime/tools/setup-envtest", "ENVTEST_VERSION"),
    ("golangci-lint", "github.com/golangci/golangci-lint/v2/cmd/golangci-lint", "GOLANGCI_LINT_VERSION"),
    ("actionlint", "github.com/rhysd/actionlint/cmd/actionlint", "ACTIONLINT_VERSION"),
    ("crd-schema-checker", "github.com/openshift/crd-schema-checker/cmd/crd-schema-checker", "CRD_SCHEMA_CHECKER_VERSION"),
    ("crd-ref-docs", "github.com/elastic/crd-ref-docs", "CRD_REF_DOCS_VERSION"),
]

# Everything the bundle captures (absolute; missing paths are skipped on save).
BUNDLE_PATHS = [
    "/usr/local/go",
    GO_MODCACHE,
    GO_BUILDCACHE,
    ENVTEST_ASSETS_DIR,
] + [f"{GOBIN}/{b}" for b in ("go", "gofmt", "helm", "kubebuilder")] + [
    f"{GOBIN}/{b}" for (b, _, _) in GO_TOOLS
]

_GO_ENV = {
    **os.environ,
    "HOME": "/root",
    "GOPATH": GOPATH_DIR,
    "GOMODCACHE": GO_MODCACHE,
    "GOCACHE": GO_BUILDCACHE,
    "GOBIN": GOBIN,
    "PATH": f"/usr/local/go/bin:{GOBIN}:" + os.environ.get("PATH", ""),
    "DEBIAN_FRONTEND": "noninteractive",
}


# Ambient env + the bundle's bin dirs on PATH, but WITHOUT forcing HOME or the
# GO*CACHE vars — so `go env -w` in the consumer writes to the job user's own
# config (read by the job command) and isn't shadowed by an OS-env override.
_PATH_ENV = {
    **os.environ,
    "PATH": f"/usr/local/go/bin:{GOBIN}:" + os.environ.get("PATH", ""),
}


def _sh(cmd, env=None):
    print(f"+ {cmd}", flush=True)
    subprocess.run(cmd, shell=True, check=True, env=env or _GO_ENV)


def _makefile_version(var):
    m = re.search(rf"^{var}\s*[:?]?=\s*(\S+)", Path("Makefile").read_text(), re.M)
    if not m:
        raise RuntimeError(f"could not find {var} in Makefile")
    return m.group(1)


def _go_version():
    m = re.search(r"^go\s+(\d+\.\d+(?:\.\d+)?)", Path("go.mod").read_text(), re.M)
    if not m:
        raise RuntimeError("could not find go version in go.mod")
    return m.group(1)


def _arch():
    return subprocess.check_output(["dpkg", "--print-architecture"], text=True).strip()


def _versions():
    v = {
        "go": _go_version(),
        "helm": HELM_VERSION,
        "kubebuilder": _makefile_version("KUBEBUILDER_VERSION"),
        "envtest_k8s": _makefile_version("ENVTEST_K8S_VERSION"),
        "codespell": _makefile_version("CODESPELL_VERSION"),
    }
    for _, _, var in GO_TOOLS:
        v[var] = _makefile_version(var)
    return v


def _install(arch, versions):
    print("Installing Go toolchain + CI tools (cache miss)...", flush=True)
    # Go SDK
    _sh(f'curl -fsSL "https://go.dev/dl/go{versions["go"]}.linux-{arch}.tar.gz" -o /tmp/go.tgz')
    _sh("rm -rf /usr/local/go && tar -C /usr/local -xzf /tmp/go.tgz && rm -f /tmp/go.tgz")
    _sh(f"ln -sf /usr/local/go/bin/go {GOBIN}/go && ln -sf /usr/local/go/bin/gofmt {GOBIN}/gofmt")
    _sh("go version")
    # helm
    _sh(f'curl -fsSL "https://get.helm.sh/helm-{versions["helm"]}-linux-{arch}.tar.gz" -o /tmp/helm.tgz')
    _sh(f"tar -xzf /tmp/helm.tgz -C /tmp && install -m 0755 /tmp/linux-{arch}/helm {GOBIN}/helm && rm -rf /tmp/helm.tgz /tmp/linux-{arch}")
    # kubebuilder
    _sh(f'curl -fsSL "https://github.com/kubernetes-sigs/kubebuilder/releases/download/{versions["kubebuilder"]}/kubebuilder_linux_{arch}" -o {GOBIN}/kubebuilder && chmod +x {GOBIN}/kubebuilder')
    # go-installed CLIs (also warms the module + build cache)
    for _, pkg, var in GO_TOOLS:
        _sh(f"go install {pkg}@{versions[var]}")
    # warm the repo's module cache
    _sh("go mod download")
    # envtest K8s assets
    _sh(f"mkdir -p {ENVTEST_ASSETS_DIR} && setup-envtest use {versions['envtest_k8s']} --bin-dir {ENVTEST_ASSETS_DIR} -p path")
    # codespell wheel into the pip cache (Makefile reinstalls it with --target)
    _sh(f"python3 -m pip install --break-system-packages codespell=={versions['codespell']}")


NAMESPACE = "go-env"
# Bump when the bundle's on-disk LAYOUT changes (paths in BUNDLE_PATHS / cache
# locations) so a layout change invalidates the key and the prep job rebuilds
# instead of restoring a stale-layout bundle under the same version key.
BUNDLE_VERSION = "2"


def _key_and_cache():
    arch = _arch()
    versions = _versions()
    key = S3PathCache.key_from(
        [f"bundle={BUNDLE_VERSION}", arch]
        + [f"{k}={v}" for k, v in sorted(versions.items())]
    )
    print(f"go-env cache key: {key} (arch={arch}, bundle=v{BUNDLE_VERSION}, versions={versions})")
    cache = None
    try:
        bucket, _, prefix = CACHE_S3_PATH.partition("/")
        cache = S3PathCache(bucket=bucket, prefix=prefix, region=AWS_REGION)
    except Exception as e:
        print(f"WARNING: S3 cache unavailable ({e})")
    return arch, versions, key, cache


def ensure():
    """Workflow pre-hook: populate the S3 bundle once (in the Config job) if it is
    missing, so the per-job pre-hooks only download instead of each rebuilding on a
    cache miss. Best-effort — on any failure the jobs still self-provision."""
    arch, versions, key, cache = _key_and_cache()
    if not cache:
        print("No S3 cache; jobs will self-provision")
        return 0
    try:
        if cache.exists(key, namespace=NAMESPACE):
            print("go-env cache already present; nothing to prepare")
            return 0
        print("go-env cache miss; building bundle once for the run")
        _install(arch, versions)
        cache.save(key, BUNDLE_PATHS, namespace=NAMESPACE)
    except Exception as e:
        print(f"WARNING: could not prepare go-env cache ({e}); jobs will self-provision")
    return 0


def setup():
    """Per-job pre-hook: restore the bundle; on a miss (e.g. the ensure step did
    not run or a different arch) build it and save it so the run self-heals."""
    arch, versions, key, cache = _key_and_cache()
    restored = False
    if cache:
        try:
            restored = cache.restore(key, namespace=NAMESPACE)
        except Exception as e:
            print(f"WARNING: cache restore failed ({e}); installing fresh")
    if not restored:
        _install(arch, versions)
        if cache:
            try:
                cache.save(key, BUNDLE_PATHS, namespace=NAMESPACE)
            except Exception as e:
                print(f"WARNING: cache save failed ({e}); continuing")
    _sh("go version && helm version && kubebuilder version && controller-gen --version")
    return 0


def _make_tarball(outpath):
    rel = [os.path.relpath(p, "/") for p in BUNDLE_PATHS if Path(p).exists()]
    Path(outpath).parent.mkdir(parents=True, exist_ok=True)
    _sh(f"tar -C / -cf - {' '.join(rel)} | zstd -c -T0 > {outpath}")


def prepare(outpath):
    """Prep-job entry point: produce the go-env bundle tarball at ``outpath`` (to
    be published as a praktika artifact). Restores it from S3 if cached, otherwise
    builds the toolchain, tars it, and uploads it to S3. One prep job per arch."""
    arch, versions, key, cache = _key_and_cache()
    if cache:
        try:
            if cache.download(key, NAMESPACE, outpath):
                print(f"go-env bundle restored from cache -> {outpath}")
                return 0
        except Exception as e:
            print(f"WARNING: cache download failed ({e}); building")
    _install(arch, versions)
    _make_tarball(outpath)
    if cache:
        try:
            cache.upload(key, NAMESPACE, outpath)
        except Exception as e:
            print(f"WARNING: cache upload failed ({e}); continuing")
    return 0


def install():
    """Consumer-job pre-hook: extract the go-env bundle that `requires` placed in
    the input dir. Falls back to self-provisioning (restore-or-build) if the
    artifact is missing, so the job still works off the happy path."""
    tarballs = sorted(glob.glob(os.path.join(PraktikaSettings.INPUT_DIR, ARTIFACT_GLOB)))
    if not tarballs:
        print("No go-env artifact in input dir; self-provisioning")
        return setup()
    tb = tarballs[0]
    print(f"Installing go-env from artifact {tb}")
    # Run with the job's ambient env (its real $HOME), so `go env -w` writes to the
    # config the job command will read, and the bundled GO*CACHE aren't shadowed by
    # an OS-env override.
    _sh(f"zstd -d {tb} --stdout | tar -xf - -C /", env=_PATH_ENV)
    _sh(
        f"go env -w GOMODCACHE={GO_MODCACHE} GOCACHE={GO_BUILDCACHE} GOPATH={GOPATH_DIR}",
        env=_PATH_ENV,
    )
    _sh(
        "go version && helm version && kubebuilder version && controller-gen --version",
        env=_PATH_ENV,
    )
    _sh("go env GOMODCACHE GOCACHE", env=_PATH_ENV)
    return 0


def main():
    mode = sys.argv[1] if len(sys.argv) > 1 else "setup"
    if mode == "prepare":
        return prepare(sys.argv[2])
    if mode == "install":
        return install()
    if mode == "ensure":
        return ensure()
    return setup()


if __name__ == "__main__":
    sys.exit(main())
