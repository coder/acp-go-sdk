#!/usr/bin/env bash
# Replay the counterexamples of check.sh against the real code.
#
# Copies replay_test.go into the package root under a scratch name, runs its
# tests, and removes the copy. Each test asserts a promise from SKILL.md.
#
# Exit codes: 0 every promise holds, 1 a replay breaks a promise, 2 setup or
# build error, or a replay that could not follow its path (a SETUP: failure).
set -euo pipefail

SKILL_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO=$(cd "$SKILL_DIR/../../.." && pwd)
COPY=$REPO/zz_session_cancel_replay_test.go
LOG=$(mktemp "${TMPDIR:-/tmp}/acp-session-cancel-replay.XXXXXX")

[ ! -e "$COPY" ] || {
	echo "replay.sh: $COPY exists; remove it first" >&2
	exit 2
}
cp "$SKILL_DIR/replay_test.go" "$COPY"
trap 'rm -f "$COPY"' EXIT

rc=0
(cd "$REPO" && go test -count=1 -v -run '^TestReplay_' .) >"$LOG" 2>&1 || rc=$?

if grep -Eq '^\s+zz_session_cancel_replay_test.go:[0-9]+: SETUP:' "$LOG"; then
	grep -E '^\s+zz_session_cancel_replay_test.go:[0-9]+: SETUP:' "$LOG" | sed 's/^\s*/  /'
	echo "replay.sh: a replay could not follow its path. Log: $LOG" >&2
	exit 2
fi
holds=$(grep -c '^--- PASS: TestReplay_' "$LOG" || true)
breaks=$(grep -c '^--- FAIL: TestReplay_' "$LOG" || true)
if [ "$rc" != 0 ] && [ "$breaks" = 0 ]; then
	echo "replay.sh: go test failed without a test result. Log: $LOG" >&2
	exit 2
fi
grep -E '^--- (PASS|FAIL): TestReplay_' "$LOG" | sed 's/^--- PASS:/holds: /; s/^--- FAIL:/breaks:/; s/ (.*)$//'
grep -E '^\s+zz_session_cancel_replay_test.go:[0-9]+: ' "$LOG" | sed 's/^\s*/  /' || true
echo "$((holds + breaks)) replays: $holds hold, $breaks break a promise. Log: $LOG"
[ "$breaks" = 0 ] || exit 1
