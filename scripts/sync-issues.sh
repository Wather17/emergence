#!/usr/bin/env bash

set -Eeuo pipefail

ISSUES_DIR="${ISSUES_DIR:-issues}"
tmp_dir=""

cleanup() {
  if [[ -n "$tmp_dir" && -d "$tmp_dir" ]]; then
    rm -rf -- "$tmp_dir"
  fi
}
trap cleanup EXIT

if ! command -v gh >/dev/null 2>&1; then
  printf 'Erro: GitHub CLI (gh) não está instalado ou não está no PATH.\n' >&2
  exit 1
fi

mkdir -p -- "$ISSUES_DIR"

printf 'Sincronizando issues abertas do GitHub...\n'

# O cache atual só é substituído depois que a consulta e todos os detalhes
# das issues terminarem com sucesso.
if ! issues=$(gh issue list --state open --limit 1000 --json number --jq '.[].number'); then
  printf 'Erro: não foi possível consultar as issues abertas. Verifique a autenticação e a conexão.\n' >&2
  exit 1
fi

tmp_dir=$(mktemp -d "${ISSUES_DIR}.tmp.XXXXXX")
count=0

for num in $issues; do
  if ! title=$(gh issue view "$num" --json title --jq '.title'); then
    printf 'Erro: não foi possível obter o título da issue #%s.\n' "$num" >&2
    exit 1
  fi

  if ! labels=$(gh issue view "$num" --json labels --jq '[.labels[].name] | join(", ")'); then
    printf 'Erro: não foi possível obter as labels da issue #%s.\n' "$num" >&2
    exit 1
  fi

  if ! body=$(gh issue view "$num" --json body --jq '.body'); then
    printf 'Erro: não foi possível obter a descrição da issue #%s.\n' "$num" >&2
    exit 1
  fi

  if ! comments=$(gh issue view "$num" --json comments --jq '.comments[] | "### Comentário por @\(.author.login):\n\(.body)\n"'); then
    printf 'Erro: não foi possível obter a discussão da issue #%s.\n' "$num" >&2
    exit 1
  fi

  slug=$(printf '%s' "$title" \
    | LC_ALL=C tr '[:upper:]' '[:lower:]' \
    | tr ' ' '-' \
    | LC_ALL=C sed -e 's/[^a-z0-9-]//g' -e 's/-\+/-/g' -e 's/^-*//' -e 's/-*$//' \
    | cut -c1-40)
  slug="${slug:-issue}"
  filename="${num}-${slug}.md"

  printf ' -> Sincronizando: #%s - %s\n' "$num" "$title"

  {
    printf '# Issue #%s: %s\n' "$num" "$title"
    if [[ -n "$labels" ]]; then
      printf '**Labels**: %s\n' "$labels"
    fi
    printf '\n## Descrição\n'
    printf '%s\n' "$body"
    printf '\n'

    if [[ -n "$comments" ]]; then
      printf '## Discussão\n'
      printf '%s\n' "$comments"
    fi
  } > "$tmp_dir/$filename"

  count=$((count + 1))
done

shopt -s nullglob
old_files=("$ISSUES_DIR"/*.md)
if ((${#old_files[@]} > 0)); then
  rm -f -- "${old_files[@]}"
fi

new_files=("$tmp_dir"/*.md)
if ((${#new_files[@]} > 0)); then
  mv -- "${new_files[@]}" "$ISSUES_DIR/"
fi

printf 'Sincronização concluída com sucesso! %d issues ativas salvas em ./%s/\n' "$count" "$ISSUES_DIR"
