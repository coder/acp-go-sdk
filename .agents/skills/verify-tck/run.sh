#!/usr/bin/env bash
# Runs the pinned ACP Test Compatibility Kit (runner/) against the
# conformance agent (agent/), then checks its report against baseline.json
# with tckcheck. With --update it rewrites baseline.json from the report
# instead, keeping the reasons, and then checks it.
#
# Environment: TCK_OUT is where the build, the report and the agent's
# working directory go (default ${TMPDIR:-/tmp}/verify-tck); UV is the uv
# binary (default uv). The report is $TCK_OUT/report.json.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
update=()
case "${1:-}" in
"") ;;
--update) update=(-update) ;;
*)
	echo "usage: $0 [--update]" >&2
	exit 2
	;;
esac

out=${TCK_OUT:-${TMPDIR:-/tmp}/verify-tck}
# GORACE separates its options with spaces, so its log_path cannot hold one.
case $out in
*[[:space:]]*)
	echo "TCK_OUT (or TMPDIR) must not contain whitespace: $out" >&2
	exit 2
	;;
esac
uv=${UV:-uv}
report=$out/report.json
mkdir -p "$out/bin" "$out/wd"
rm -f "$report"

# The prompt texts that start the agent's slow turn, its subagent and its
# cancellable subagent. The TCK sends them and the agent keys on them, so
# both get them from here.
cancel_prompt="TCK: stream until the turn is cancelled."
subagent_prompt="TCK: delegate to a subagent that finishes."
subagent_cancel_prompt="TCK: delegate to a subagent that runs until it is cancelled."

# The agent is built with the race detector: the TCK starts a fresh process
# per test and writes its first line at once, which reaches code that the
# SDK's own tests, which write only after a connection is built, do not.
# Each agent process writes a race it finds to $out/race/agent.<pid>.
(cd "$here" && go build -race -o "$out/bin/agent" ./agent && go build -o "$out/bin/tckcheck" ./tckcheck)
rm -rf "$out/race"
mkdir -p "$out/race"

# acp-tck exits 1 when the agent is not conformant, which the baseline may
# expect (a CAPABILITY row the SDK cannot pass yet), so tckcheck decides.
# --timeout bounds each response and each whole prompt turn; it must stay
# above the agent's streamLimit (agent/turns.go).
started=$(date +%s)
status=0
GORACE="log_path=$out/race/agent" "$uv" run --locked --project "$here/runner" acp-tck \
	--protocol-version 1 \
	--agent-cwd "$out/wd" \
	--auth-method tck --allow-logout \
	--cancel-prompt "$cancel_prompt" \
	--subagent-prompt "$subagent_prompt" \
	--subagent-cancel-prompt "$subagent_cancel_prompt" \
	--timeout 30 --test-timeout 120 \
	--report-json "$report" \
	-- "$out/bin/agent" \
	-cancel-prompt "$cancel_prompt" \
	-subagent-prompt "$subagent_prompt" \
	-subagent-cancel-prompt "$subagent_cancel_prompt" ||
	status=$?
echo "acp-tck exited $status after $(($(date +%s) - started))s; report: $report"
if [ ! -s "$report" ]; then
	echo "acp-tck wrote no report" >&2
	exit 1
fi
races=("$out"/race/agent.*)
if [ -e "${races[0]}" ]; then
	echo "the race detector found a data race in ${#races[@]} agent process(es); the first:" >&2
	head -n 60 "${races[0]}" >&2
	exit 1
fi
"$out/bin/tckcheck" ${update[@]+"${update[@]}"} -report "$report" -baseline "$here/baseline.json"
