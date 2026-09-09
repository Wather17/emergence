#!/usr/bin/env bash

set -Eeuo pipefail

workflow_dir="${WORKFLOW_DIR:-.github/workflows}"
matches=$(grep -HnE '^[[:space:]]*(-[[:space:]]*)?uses:[[:space:]]*[^#[:space:]]+' "$workflow_dir"/*.yml || true)
failed=0
while IFS=: read -r file line rest; do
  [[ -n "$file" ]] || continue
  action=$(printf '%s\n' "$rest" | sed -E 's/.*uses:[[:space:]]*([^#[:space:]]+).*/\1/')
  # Local reusable workflows are resolved from this repository.
  if [[ "$action" == ./* ]]; then
    continue
  fi
  ref="${action##*@}"
  if [[ "$action" == "$ref" || ! "$ref" =~ ^[[:xdigit:]]{40}$ ]]; then
    printf 'Ação sem SHA imutável em %s:%s: %s\n' "$file" "$line" "$action" >&2
    failed=1
  fi
done <<< "$matches"

if ((failed)); then
  exit 1
fi

printf 'Todas as actions externas estão fixadas em SHAs imutáveis.\n'
