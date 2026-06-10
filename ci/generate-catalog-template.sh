#!/usr/bin/env bash
set -euo pipefail

# Generate OLM catalog template with all available bundles from registry
# Usage: ./ci/generate-catalog-template.sh [<bundle-image>]

# Image repository
BUNDLE_IMAGE=${1-ghcr.io/clickhouse/clickhouse-operator-bundle}
# Output file
OUTPUT_FILE="catalog/clickhouse-operator-template.yaml"

# Function to get all tags from ghcr.io
get_bundle_tags() {
    local owner="clickhouse"
    local gh_api_url="https://api.github.com/orgs/clickhouse/packages/container/clickhouse-operator-bundle/versions?per_page=100"
    local filter="$1"
    local headers_file next_url
    headers_file=$(mktemp)
    next_url="${gh_api_url}"

    echo "Fetching bundle tags from ${BUNDLE_IMAGE}" >&2
    # grep guarded with `|| true` so a no-match (exit 1) does not abort the
    # surrounding `$(...)` under `set -e -o pipefail`.
    {
        while [ -n "${next_url}" ]; do
            curl -sL \
                -D "${headers_file}" \
                -H "Accept: application/vnd.github+json" \
                -H "X-GitHub-Api-Version: 2022-11-28" \
                -H "Authorization: Bearer ${GITHUB_TOKEN}" \
                "${next_url}" 2>/dev/null \
                | jq -r '.[].metadata.container.tags[]' 2>/dev/null
            # Follow `Link: <url>; rel="next"` until exhausted; multi-arch+attestation rows overflow page size.
            next_url=$(grep -i '^link:' "${headers_file}" | sed -n 's/.*<\([^>]*\)>; rel="next".*/\1/p' | tr -d '\r')
        done
        rm -f "${headers_file}"
    } \
        | { grep -E "$filter" || true; } \
        | sort -V
}

# Create catalog directory if it doesn't exist
mkdir -p catalog

# Generate the template YAML
cat > "$OUTPUT_FILE" <<EOF
Schema: olm.semver
GenerateMajorChannels: true
GenerateMinorChannels: false
Stable:
  Bundles:
EOF

# Get release bundle tags
BUNDLE_TAGS=$(get_bundle_tags '^v[0-9]+\.[0-9]+\.[0-9]+$')
if [ -z "$BUNDLE_TAGS" ]; then
    echo "Error: No bundle tags found in registry"
    exit 1
fi

echo "Found bundle tags:"
echo "$BUNDLE_TAGS"
for tag in $BUNDLE_TAGS; do
    if [ -n "$tag" ]; then
        echo "    - Image: ${BUNDLE_IMAGE}:${tag}" >> "$OUTPUT_FILE"
    fi
done

# Get fast bundle tags
BUNDLE_TAGS=$(get_bundle_tags '^v[0-9]+\.[0-9]+\.[0-9]+-[a-z0-9.]+$')
if [ -z "$BUNDLE_TAGS" ]; then
    echo "Error: No fast bundle tags found in registry"
    exit 1
fi

echo "Found fast bundle tags:"
echo "$BUNDLE_TAGS"

# Generate the template YAML
cat >> "$OUTPUT_FILE" <<EOF
Fast:
  Bundles:
EOF

for tag in $BUNDLE_TAGS; do
    if [ -n "$tag" ]; then
        echo "    - Image: ${BUNDLE_IMAGE}:${tag}" >> "$OUTPUT_FILE"
    fi
done

echo ""
echo "Generated catalog template at: $OUTPUT_FILE"
echo "Contents:"
cat "$OUTPUT_FILE"
