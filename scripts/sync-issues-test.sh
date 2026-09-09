#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
tmp_root=$(mktemp -d)
trap 'rm -rf -- "$tmp_root"' EXIT
mkdir -p -- "$tmp_root/bin" "$tmp_root/cache"

cat > "$tmp_root/bin/gh" <<'FAKE_GH'
#!/usr/bin/env bash
set -Eeuo pipefail
if [[ "$1" == issue && "$2" == list ]]; then
  printf '1\n'
  exit 0
fi
case "$*" in
  *".title"*) printf '[Test] Issue\n' ;;
  *".labels"*) printf 'bug\n' ;;
  *".body"*) printf 'body\n' ;;
  *".comments[]"*) : ;;
  *) exit 1 ;;
esac
FAKE_GH
chmod +x "$tmp_root/bin/gh"

printf 'Markdown do usuário\n' > "$tmp_root/cache/unmanaged.md"
PATH="$tmp_root/bin:$PATH" ISSUES_DIR="$tmp_root/cache" "$script_dir/sync-issues.sh"

test -f "$tmp_root/cache/unmanaged.md"
test -f "$tmp_root/cache/1-test-issue.md"
test -f "$tmp_root/cache/.emergence-issues-manifest"
printf 'Teste do sincronizador concluído: Markdown não gerenciado preservado.\n'
