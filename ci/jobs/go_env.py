#!/usr/bin/env python3
"""
Provisions the Go toolchain + CI tools for the Operator CI jobs, distributed as a
praktika artifact (one per arch) so version bumps need no AMI rebuild.

`prepare` builds the bundle and writes the tarball for praktika to publish;
`install` extracts the required artifact in a consumer job's pre-hook.

Provides: the Go SDK (/usr/local/go), helm + kubebuilder + go-installed CLIs
(controller-gen, kustomize, setup-envtest, golangci-lint, actionlint,
crd-schema-checker, crd-ref-docs) on PATH, a warm Go module + build cache under
/opt/ci-go, and the envtest K8s assets. Versions come from go.mod / Makefile
(helm is pinned here).
"""
import glob
import os
import re
import subprocess
import sys
from pathlib import Path

sys.path.insert(0, os.getcwd())  # repo root importable for ci.*
from praktika.settings import Settings as PraktikaSettings

ARTIFACT_GLOB = "go-env*.tar.zst"

HELM_VERSION = "v3.19.0"  # no Makefile pin; bump here
ENVTEST_ASSETS_DIR = "/opt/ci-go/envtest"
GOBIN = "/usr/local/bin"
# Fixed $HOME-independent cache paths: the consumer job runs under a different
# $HOME than the build, so `go env -w` points the job's `go` at these.
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

# Bundle contents (absolute; missing paths skipped when tarring).
BUNDLE_PATHS = [
    "/usr/local/go",
    GO_MODCACHE,
    GO_BUILDCACHE,
    ENVTEST_ASSETS_DIR,
] + [f"{GOBIN}/{b}" for b in ("go", "gofmt", "helm", "kubebuilder")] + [
    f"{GOBIN}/{b}" for (b, _, _) in GO_TOOLS
]

# Build env: pins HOME + GO*CACHE so the build writes into the bundle paths.
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

# Consumer env: bundle bins on PATH, but no HOME/GO*CACHE override so `go env -w`
# writes the job user's own config.
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
    print("Installing Go toolchain + CI tools...", flush=True)
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
    _sh("go mod download")
    _sh(f"mkdir -p {ENVTEST_ASSETS_DIR} && setup-envtest use {versions['envtest_k8s']} --bin-dir {ENVTEST_ASSETS_DIR} -p path")
    # codespell is not bundled; warms pip for a fresh-build fallback only.
    _sh(f"python3 -m pip install --break-system-packages codespell=={versions['codespell']}")


def _make_tarball(outpath):
    rel = [os.path.relpath(p, "/") for p in BUNDLE_PATHS if Path(p).exists()]
    Path(outpath).parent.mkdir(parents=True, exist_ok=True)
    _sh(f"tar -C / -cf - {' '.join(rel)} | zstd -c -T0 > {outpath}")


def prepare(outpath):
    """Build the toolchain and write the bundle tarball to ``outpath``."""
    _install(_arch(), _versions())
    _make_tarball(outpath)
    return 0


def install():
    """Extract the required bundle from INPUT_DIR and point the job's `go` at the
    bundled caches; build fresh if no artifact is present (e.g. a local run)."""
    tarballs = sorted(glob.glob(os.path.join(PraktikaSettings.INPUT_DIR, ARTIFACT_GLOB)))
    if tarballs:
        print(f"Installing go-env from artifact {tarballs[0]}")
        _sh(f"zstd -d {tarballs[0]} --stdout | tar -xf - -C /", env=_PATH_ENV)
    else:
        print("No go-env artifact in input dir; building fresh")
        _install(_arch(), _versions())
    _sh(f"go env -w GOMODCACHE={GO_MODCACHE} GOCACHE={GO_BUILDCACHE} GOPATH={GOPATH_DIR}", env=_PATH_ENV)
    _sh("go version && helm version && kubebuilder version && controller-gen --version", env=_PATH_ENV)
    _sh("go env GOMODCACHE GOCACHE", env=_PATH_ENV)
    return 0


def main():
    mode = sys.argv[1] if len(sys.argv) > 1 else "install"
    if mode == "prepare":
        return prepare(sys.argv[2])
    if mode == "install":
        return install()
    raise SystemExit(f"unknown mode: {mode!r} (expected 'prepare' or 'install')")


if __name__ == "__main__":
    sys.exit(main())
