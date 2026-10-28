#!/usr/bin/env bash
# PostToolUse hook: after Claude changes Go code (an Edit or Write of a .go
# file or go.mod, or a gosym edit), run strictcheck's fast steps and hand
# any findings back to Claude, which sees this hook's stderr on exit 2.
#
# STRICTCHECK overrides the command, e.g. STRICTCHECK=strictcheck when it is
# installed with go install rather than as a go tool.
set -u
command -v jq >/dev/null || { echo "check-after-edit.sh: jq is not installed" >&2; exit 1; }

input=$(cat)
case "$(jq -r '.tool_name' <<<"$input")" in
Bash)
	jq -r '.tool_input.command // ""' <<<"$input" |
		grep -qE '(^|[^[:alnum:]_-])gosym[[:space:]]+(replace|add|delete)' || exit 0
	;;
*)
	case "$(jq -r '.tool_input.file_path // ""' <<<"$input")" in
	*.go | go.mod | */go.mod) ;;
	*) exit 0 ;;
	esac
	;;
esac

cd "${CLAUDE_PROJECT_DIR:-.}" || exit 1
# shellcheck disable=SC2086 # STRICTCHECK may be a command with arguments
if out=$(${STRICTCHECK:-go tool strictcheck} -steps compile,analyze,format -format text 2>&1); then
	exit 0
fi
printf 'strictcheck reports problems with the code as it now is; fix them before going on:\n%s\n' "$out" >&2
exit 2
