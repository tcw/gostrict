#!/usr/bin/env bash
# Stop hook: before Claude finishes, run every strictcheck step and, if the
# report is not ok, send Claude back to fix it (exit 2; Claude sees stderr).
#
# A passing state is remembered in .git/strictcheck-ok, so a turn that did
# not touch Go code since the last passing run does not run the tests again.
# STRICTCHECK overrides the command, as in check-after-edit.sh.
set -u
command -v jq >/dev/null || { echo "check-before-stop.sh: jq is not installed" >&2; exit 1; }

# Claude is already continuing because this hook stopped it once; let it
# stop now rather than loop. The report it was given is still unresolved,
# and says so in the transcript.
[ "$(jq -r '.stop_hook_active // false')" = true ] && exit 0

cd "${CLAUDE_PROJECT_DIR:-.}" || exit 1

stamp="" state=""
if stamp=$(git rev-parse --git-path strictcheck-ok 2>/dev/null); then
	state=$({
		git rev-parse HEAD
		git diff HEAD -- '*.go' go.mod go.sum
		git ls-files --others --exclude-standard -z -- '*.go' | xargs -0 -r cat
	} 2>/dev/null | git hash-object --stdin)
	if [ -f "$stamp" ] && [ "$(cat "$stamp")" = "$state" ]; then
		exit 0
	fi
fi

# shellcheck disable=SC2086 # STRICTCHECK may be a command with arguments
if out=$(${STRICTCHECK:-go tool strictcheck} -format text 2>&1); then
	[ -n "$stamp" ] && printf '%s\n' "$state" >"$stamp"
	exit 0
fi
printf 'Not done: strictcheck reports problems. Fix them, then finish:\n%s\n' "$out" >&2
exit 2
