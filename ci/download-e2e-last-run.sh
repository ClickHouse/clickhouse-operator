#!/usr/bin/env bash

set -euo pipefail

if [ "$#" -ne 1 ]; then
  echo "usage: $0 <output-dir>" >&2
  exit 2
fi

output_dir=$1
selected_dir="$output_dir/latest"

mkdir -p "$output_dir"
rm -rf "$selected_dir"

if ! run_ids=$(gh run list \
  --repo "$GITHUB_REPOSITORY" \
  --workflow ci.yaml \
  --branch main \
  --event push \
  --status success \
  --limit 20 \
  --json databaseId \
  --jq '.[].databaseId'); then
  echo "Unable to fetch the previous e2e run; using equal shard weights."
  exit 0
fi

while IFS= read -r run_id; do
  if [ -z "$run_id" ]; then
    continue
  fi

  candidate_dir=$(mktemp -d "$output_dir/run.XXXXXX")
  complete=1
  for shard in 1 2 3 4; do
    report_dir="$candidate_dir/e2e-report-shard-$shard"
    if ! gh run download \
      "$run_id" \
      --repo "$GITHUB_REPOSITORY" \
      --name "e2e-report-shard-$shard" \
      --dir "$report_dir"; then
      complete=0
      break
    fi

    if ! find "$report_dir" -name ginkgo-report.json -print -quit | grep -q .; then
      complete=0
      break
    fi
  done

  if [ "$complete" -eq 1 ]; then
    mv "$candidate_dir" "$selected_dir"
    echo "Downloaded Ginkgo timings from successful main e2e run $run_id."
    exit 0
  fi

  rm -rf "$candidate_dir"
done <<< "$run_ids"

echo "No successful main e2e run with all Ginkgo reports found; using equal shard weights."
