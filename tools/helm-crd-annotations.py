#!/usr/bin/env python3
"""Restore crd.annotations after the kubebuilder Helm plugin regenerates dist/chart.

The plugin emits crd.enabled and crd.keep only. This step merges a caller-supplied
annotation map onto both CRDs so GitOps tools can mark those objects for
server-side apply. generate-helmchart runs this script, and Helm CI fails if the
committed chart drifts from that output.
"""

from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
CHART = ROOT / "dist" / "chart"

CRD_BLOCK = (
    "    {{- with .Values.crd.annotations }}\n"
    "    {{- toYaml . | nindent 4 }}\n"
    "    {{- end }}\n"
)
CRD_ANCHOR = "    controller-gen.kubebuilder.io/version:"

VALUES_ANCHOR = (
    "crd:\n"
    "  # Install CRDs with the chart\n"
    "  enabled: true\n"
    "  # Keep CRDs when uninstalling\n"
    "  keep: true\n"
)
VALUES_INSERT = (
    "  # Extra annotations added to both CRDs.\n"
    "  # Example: argocd.argoproj.io/sync-options: ServerSideApply=true\n"
    "  annotations: {}\n"
)


def crd_text(path: Path, text: str) -> str:
    if ".Values.crd.annotations" in text:
        return text
    index = text.find(CRD_ANCHOR)
    if index == -1:
        raise SystemExit(f"{path}: controller-gen annotation line not found")
    return text[:index] + CRD_BLOCK + text[index:]


def values_text(path: Path, text: str) -> str:
    if VALUES_INSERT in text:
        return text
    if VALUES_ANCHOR not in text:
        raise SystemExit(f"{path}: crd values block not found")
    return text.replace(VALUES_ANCHOR, VALUES_ANCHOR + VALUES_INSERT, 1)


def main() -> None:
    crds = sorted((CHART / "templates" / "crd").glob("*.yaml"))
    if len(crds) != 2:
        raise SystemExit(f"expected 2 CRD templates, found {len(crds)}")
    values = CHART / "values.yaml"
    # Validate every input before writing, so a bad file does not leave a half-updated chart.
    updated = [(path, crd_text(path, path.read_text())) for path in crds]
    updated.append((values, values_text(values, values.read_text())))
    for path, text in updated:
        path.write_text(text)


if __name__ == "__main__":
    main()
