#!/usr/bin/env bash
# PreToolUse hook for Write: refuse to overwrite an existing Go file, so
# changes go through gosym or a targeted Edit instead of a whole-file
# rewrite, which silently drops code the model did not mean to touch.
set -u
command -v jq >/dev/null || { echo "no-go-rewrite.sh: jq is not installed" >&2; exit 1; }

file=$(jq -r '.tool_input.file_path // ""')
case "$file" in *.go) ;; *) exit 0 ;; esac
[ -e "$file" ] || exit 0

cat >&2 <<MSG
$file already exists; do not rewrite whole Go files. Change one declaration at a time:
  go tool gosym show <dir> <Name>       read it
  go tool gosym replace <dir> <Name>    replace it, with the complete new declaration on stdin
  go tool gosym add / delete            add or remove one
or make a targeted Edit.
MSG
exit 2
