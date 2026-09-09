#!/usr/bin/env bash

set -Eeuo pipefail

ISSUES_DIR="${ISSUES_DIR:-issues}"
if [[ "$ISSUES_DIR" = /* ]]; then
  issues_path="$ISSUES_DIR"
else
  issues_path="$PWD/$ISSUES_DIR"
fi
tmp_dir=""
manifest_name='.emergence-issues-manifest'

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

if [[ -L "$issues_path" ]]; then
  printf 'Erro: o diretório do cache não pode ser um link: %s\n' "$issues_path" >&2
  exit 1
fi
mkdir -p -- "$issues_path"
if [[ -L "$issues_path" ]]; then
  printf 'Erro: o diretório do cache tornou-se um link durante a preparação.\n' >&2
  exit 1
fi
issues_path=$(cd -- "$issues_path" && pwd -P)
manifest="$issues_path/$manifest_name"

declare -A managed=()
if [[ -e "$manifest" ]]; then
  if [[ -L "$manifest" || ! -f "$manifest" ]]; then
    printf 'Erro: o manifesto do cache não é um arquivo regular: %s\n' "$manifest" >&2
    exit 1
  fi
  while IFS= read -r name; do
    [[ -z "$name" || "$name" == '# emergence issues manifest v1' ]] && continue
    if [[ "$name" == */* || "$name" != *.md ]]; then
      printf 'Erro: manifesto do cache contém nome inválido: %s\n' "$name" >&2
      exit 1
    fi
    managed["$name"]=1
  done < "$manifest"
fi

printf 'Sincronizando issues abertas do GitHub...\n'

# O cache atual só é substituído depois que a consulta e todos os detalhes
# das issues terminarem com sucesso.
if ! issues=$(gh issue list --state open --limit 1000 --json number --jq '.[].number'); then
  printf 'Erro: não foi possível consultar as issues abertas. Verifique a autenticação e a conexão.\n' >&2
  exit 1
fi

tmp_dir=$(mktemp -d "$issues_path/.tmp.XXXXXX")
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
new_files=("$tmp_dir"/*.md)
new_names=()
for path in "${new_files[@]}"; do
  name=$(basename -- "$path")
  new_names+=("$name")
  destination="$issues_path/$name"
  if [[ -e "$destination" && -z "${managed[$name]+x}" ]]; then
    printf 'Erro: o arquivo Markdown não gerenciado já existe no cache: %s\n' "$destination" >&2
    exit 1
  fi
  if [[ -L "$destination" || ( -e "$destination" && ! -f "$destination" ) ]]; then
    printf 'Erro: destino do cache não é um arquivo regular: %s\n' "$destination" >&2
    exit 1
  fi
done

# Remove only files recorded by the previous successful synchronization.
for name in "${!managed[@]}"; do
  destination="$issues_path/$name"
  if [[ -L "$destination" || ( -e "$destination" && ! -f "$destination" ) ]]; then
    printf 'Erro: arquivo gerenciado foi substituído por um tipo inseguro: %s\n' "$destination" >&2
    exit 1
  fi
  if [[ -e "$destination" ]]; then
    rm -f -- "$destination"
  fi
done

for path in "${new_files[@]}"; do
  mv -- "$path" "$issues_path/"
done

manifest_stage="$tmp_dir/$manifest_name"
{
  printf '# emergence issues manifest v1\n'
  printf '%s\n' "${new_names[@]}"
} > "$manifest_stage"
mv -- "$manifest_stage" "$manifest"
sync 2>/dev/null || true

printf 'Sincronização concluída com sucesso! %d issues ativas salvas em ./%s/\n' "$count" "$ISSUES_DIR"
