#!/usr/bin/env bash
# Model-check session/cancel handling (agent.go, agent_gen.go) with TLC.
#
#   check.sh [--out DIR]            run the checks, keep TLC output in DIR
#   check.sh --update-fingerprint   record the covered code as synced
#
# Exit codes:
#   0  every config gave its expected result and the covered code matches
#      fingerprint.txt
#   1  a config gave a result other than the expected one: read the TLC
#      output named in the table
#   2  setup or drift error: a tool is missing, or the code no longer has a
#      shape that the model assumes
#   3  every result was as expected, but covered code changed since the sync
#      commit: review the mapping in SKILL.md
set -euo pipefail
# shellcheck disable=SC2329 # called by the ERR trap
on_err() {
	local rc=$?
	[ "$rc" = 2 ] || echo "check.sh: unexpected failure (exit $rc) near line $1" >&2
	exit 2
}
trap 'on_err $LINENO' ERR

SKILL_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO=$(cd "$SKILL_DIR/../../.." && pwd)
SPEC=SessionCancel

# TLC 1.7.4 is the last immutable TLA+ release. verify-notification-barrier
# pins the same jar and shares the cache.
TLA_VERSION=1.7.4
TLA_SHA256=936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88
TLA_URL=https://github.com/tlaplus/tlaplus/releases/download/v$TLA_VERSION/tla2tools.jar

# config:expected result. "pass", or the invariant that the current code
# breaks (a known finding, see SKILL.md).
CONFIGS=(
	one-prompt-at-a-time.cfg:pass
	two-sessions.cfg:pass
	peer-wait-earlier-prompt.cfg:pass
	overlapping-prompts.cfg:pass
	peer-wait-later-prompt.cfg:pass
	cancel-waits-later-prompt.cfg:pass
)

# file|start of the block. A block runs from its first line to the closing
# brace of a top-level func, or to the next case of the dispatch switch.
COVERED=(
	"agent.go|func NewAgentSideConnection("
	"agent.go|func (a *AgentSideConnection) cancelPrompt("
	"agent.go|func (a *AgentSideConnection) registerPrompt("
	"agent.go|func (a *AgentSideConnection) pruneCancelMarkers("
	"agent_gen.go|	case AgentMethodSessionCancel:"
	"agent_gen.go|	case AgentMethodSessionPrompt:"
	"extensions.go|func (a *AgentSideConnection) handleWithExtensions("
	"connection.go|func NewConnection("
	"connection.go|func (c *Connection) notificationSeq("
	"connection.go|func (c *Connection) requestWatermark("
	"connection.go|func (c *Connection) requestArrival("
	"connection.go|func (c *Connection) receive("
	"connection.go|func (c *Connection) processNotifications("
	"connection.go|func (c *Connection) handleInbound("
	"connection.go|func SendRequest[T any](c *Connection,"
	"connection.go|func (c *Connection) SendRequestNoResult("
	"connection.go|func (c *Connection) waitNotifications("
	"connection.go|func (c *Connection) waitInboundBarrier("
	"connection.go|func (c *Connection) startPeerWait("
	"connection.go|func (c *Connection) oldestInflightWatermark("
)

die() {
	echo "check.sh: $*" >&2
	exit 2
}

drift() {
	echo "DRIFT: $*" >&2
	echo "The model no longer matches the code. Re-read the mapping in SKILL.md." >&2
	exit 2
}

# block FILE START: print a covered block (see COVERED).
block() {
	START="$2" awk '
		!on && index($0, ENVIRON["START"]) == 1 { on = 1; print; c = ($0 ~ /^\tcase /); next }
		on && c && /^\t(case |default:)/ { exit }
		on { print }
		on && !c && /^}$/ { exit }
	' "$REPO/$1"
}

need() {
	local b
	b=$(block "$1" "$2")
	[ -n "$b" ] || drift "not found in $1: $2"
	printf '%s\n' "$b"
}

has() { grep -Fq -- "$2" <<<"$1"; }
line_of() { { grep -Fn -- "$2" <<<"$1" || true; } | head -n 1 | cut -d: -f1; }
# line_at BODY N: line N of BODY without leading tabs.
line_at() { sed -n "$2p" <<<"$1" | sed 's/^[[:space:]]*//'; }
# in_order BODY LINE...: each LINE occurs after the match of the one before it.
in_order() {
	local b=$1 prev=0 l s
	shift
	for s in "$@"; do
		l=$(S="$s" P="$prev" awk 'NR > ENVIRON["P"] && index($0, ENVIRON["S"]) { print NR; exit }' <<<"$b")
		[ -n "$l" ] || return 1
		prev=$l
	done
}

fingerprint() {
	local entry
	for entry in "${COVERED[@]}"; do
		printf '%s  %s\n' "$(need "${entry%%|*}" "${entry#*|}" | sha256sum | cut -d' ' -f1)" "$entry"
	done
}

for f in agent.go agent_gen.go extensions.go connection.go; do
	[ -f "$REPO/$f" ] || die "missing $REPO/$f"
done

if [ "${1:-}" = "--update-fingerprint" ]; then
	fingerprint >"$SKILL_DIR/fingerprint.txt"
	echo "wrote $SKILL_DIR/fingerprint.txt"
	echo "Commit it with the model change. That commit becomes the sync commit."
	exit 0
fi

OUT=""
while [ $# -gt 0 ]; do
	case "$1" in
	--out)
		shift
		OUT=${1:?--out needs a directory}
		;;
	*) die "unknown argument: $1" ;;
	esac
	shift
done
[ -n "$OUT" ] || OUT=$(mktemp -d "${TMPDIR:-/tmp}/acp-session-cancel-tlc.XXXXXX")
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)

# ---- Read the facts that the model depends on ------------------------------

B_asc=$(need agent.go "func NewAgentSideConnection(")
B_cancel=$(need agent.go "func (a *AgentSideConnection) cancelPrompt(")
B_reg=$(need agent.go "func (a *AgentSideConnection) registerPrompt(")
B_prune=$(need agent.go "func (a *AgentSideConnection) pruneCancelMarkers(")
B_ccase=$(need agent_gen.go "	case AgentMethodSessionCancel:")
B_pcase=$(need agent_gen.go "	case AgentMethodSessionPrompt:")
B_ext=$(need extensions.go "func (a *AgentSideConnection) handleWithExtensions(")
B_newc=$(need connection.go "func NewConnection(")
B_hin=$(need connection.go "func (c *Connection) handleInbound(")
B_send=$(need connection.go "func SendRequest[T any](c *Connection,")
B_sendnr=$(need connection.go "func (c *Connection) SendRequestNoResult(")
B_nseq=$(need connection.go "func (c *Connection) notificationSeq(")
B_rwm=$(need connection.go "func (c *Connection) requestWatermark(")
B_rarr=$(need connection.go "func (c *Connection) requestArrival(")
B_recv=$(need connection.go "func (c *Connection) receive(")
B_proc=$(need connection.go "func (c *Connection) processNotifications(")
B_wait=$(need connection.go "func (c *Connection) waitNotifications(")
B_inb=$(need connection.go "func (c *Connection) waitInboundBarrier(")
B_peer=$(need connection.go "func (c *Connection) startPeerWait(")
B_old=$(need connection.go "func (c *Connection) oldestInflightWatermark(")

# Shapes the model assumes. A change here needs a model change, not a fact.
in_order "$B_cancel" "a.mu.Lock()" "seq, hasSeq := a.conn.notificationSeq(ctx)" "if pc, ok := a.sessionCancels[sessionID]; ok" \
	"pc.cancel()" "delete(a.sessionCancels, sessionID)" "a.pruneCancelMarkers()" ||
	drift "cancelPrompt no longer cancels and deletes a.sessionCancels[sessionID] under a.mu, then prunes (RunCancel)"
in_order "$B_reg" "a.mu.Lock()" "watermark, hasWatermark := a.conn.requestWatermark(ctx)" \
	"arrival, _ := a.conn.requestArrival(ctx)" \
	"pc := &promptCancel{cancel: cancel, watermark: watermark, hasWatermark: hasWatermark, arrival: arrival}" \
	"if prev, ok := a.sessionCancels[sessionID]; ok {" "prev.cancel()" \
	"a.sessionCancels[sessionID] = pc" "a.pruneCancelMarkers()" ||
	drift "registerPrompt no longer cancels the previous prompt, stores its cancel, watermark and arrival under a.mu, then prunes (Register)"
in_order "$B_prune" "oldest, ok := a.conn.oldestInflightWatermark()" "for sessionID, cancelledAt := range a.sessionCancelledAt {" ||
	drift "pruneCancelMarkers no longer scans the markers against oldestInflightWatermark (Prune)"
in_order "$B_old" "for w := range c.inflightWatermarks {" "if !ok || w < oldest {" ||
	drift "oldestInflightWatermark no longer returns the smallest in-flight watermark (Prune)"
in_order "$B_ccase" "a.cancelPrompt(ctx, string(p.SessionId))" "a.agent.Cancel(ctx, p)" ||
	drift "the session/cancel case no longer calls cancelPrompt before Cancel (RunCancel)"
in_order "$B_pcase" "reqCtx, cancel = context.WithCancel(ctx)" "donePrompt := a.registerPrompt(ctx, string(p.SessionId), cancel)" \
	"resp, err := a.agent.Prompt(reqCtx, p)" "donePrompt()" ||
	drift "the session/prompt case changed its register, Prompt or cleanup steps (Register, PromptReturns, Cleanup)"
in_order "$B_reg" "return func() {" "a.mu.Lock()" "delete(a.sessionCancels, sessionID)" ||
	drift "the func that registerPrompt returns no longer deletes the entry under a.mu (Cleanup)"
has "$B_nseq" "ctx.Value(handlingNotificationKey{}).(handlingNotification)" ||
	drift "notificationSeq no longer reads the handlingNotification ctx value"
has "$B_rwm" "ctx.Value(handlingRequestKey{}).(handlingRequest)" ||
	drift "requestWatermark no longer reads the handlingRequest ctx value"
in_order "$B_rarr" "ctx.Value(handlingRequestKey{}).(handlingRequest)" "return h.arrival, true" ||
	drift "requestArrival no longer reads the arrival from the handlingRequest ctx value"
in_order "$B_proc" "handlingNotification{conn: c, seq: queued.seq}" "c.handleInbound(handlerCtx, queued.msg)" \
	"c.completedNotificationSeq = queued.seq" ||
	drift "processNotifications no longer runs each handler with its seq, then completes it (RunCancel, CompleteNotification)"
[ "$(grep -Fc "go c.processNotifications()" <<<"$B_newc")" = 1 ] && [ "$(grep -Fc "go c.receive()" <<<"$B_newc")" = 1 ] ||
	drift "NewConnection no longer starts one receive and one processNotifications goroutine"
has "$B_asc" "NewConnection(asc.handleWithExtensions," ||
	drift "NewAgentSideConnection no longer dispatches through handleWithExtensions"
in_order "$B_ext" "h.HandleExtensionMethod(ctx, method, params)" "return a.handle(ctx, method, params)" ||
	drift "handleWithExtensions no longer passes the handler ctx on (StartPeerNote, RunCancel, Register)"
in_order "$B_hin" "result, err := c.handler(ctx, req.Method, req.Params)" "_ = c.sendMessage(res)" ||
	drift "handleInbound no longer runs the handler with its ctx, then sends the response (Respond)"
in_order "$B_recv" "watermark := c.lastEnqueuedNotificationSeq" "c.inboundRequests++" \
	"handlingRequest{conn: c, watermark: watermark, arrival: c.inboundRequests}" "c.inflightWatermarks[watermark]++" "go func(" \
	"c.inflightWatermarks[watermark]--" "c.handleInbound(reqCtx, m)" ||
	drift "receive no longer records the watermark and the in-flight request before the request goroutine (ReceivePrompt, ReceiveAddsInflight, Exit)"
in_order "$B_recv" "c.lastEnqueuedNotificationSeq++" "case c.notificationQueue <- queuedNotification{seq: seq, msg: &m}:" ||
	drift "receive no longer numbers and queues notifications (ReceiveNotification)"

# MarkerCancelsWhen: registerPrompt cancels at once when a later cancel ran.
if has "$B_reg" "&& cancelledAt > watermark {"; then
	MarkerCancelsWhen=gt
	l_cmp=$(line_of "$B_reg" "&& cancelledAt > watermark {")
elif has "$B_reg" "&& cancelledAt >= watermark {"; then
	MarkerCancelsWhen=ge
	l_cmp=$(line_of "$B_reg" "&& cancelledAt >= watermark {")
elif ! has "$B_reg" "a.sessionCancelledAt[sessionID]"; then
	MarkerCancelsWhen=never
	l_cmp=""
else
	drift "registerPrompt reads sessionCancelledAt in a new form"
fi
if [ -n "$l_cmp" ]; then
	[ "$(line_at "$B_reg" $((l_cmp + 1)))" = "cancel()" ] || drift "registerPrompt no longer calls cancel() when the marker matches"
	has "$B_reg" "a.conn.requestWatermark(ctx)" || drift "registerPrompt no longer compares against requestWatermark(ctx)"
fi

# CancelRecordsMarker: cancelPrompt stores the cancel's seq.
l_seq=$(line_of "$B_cancel" "if hasSeq {")
l_set=$(line_of "$B_cancel" "a.sessionCancelledAt[sessionID] = seq")
if [ -n "$l_seq" ] && [ -n "$l_set" ] && [ "$l_set" = $((l_seq + 1)) ]; then
	CancelRecordsMarker=TRUE
elif ! has "$B_cancel" "sessionCancelledAt[sessionID] ="; then
	CancelRecordsMarker=FALSE
else
	drift "cancelPrompt stores sessionCancelledAt in a new form"
fi

# CancelSkipsLaterPrompt: cancelPrompt leaves a registered prompt that the
# client sent after the cancel (#72 item 1).
case "$(sed -n 's/^[[:space:]]*\(if pc, ok := a.sessionCancels\[sessionID\]; ok.*\)$/\1/p' <<<"$B_cancel" | head -n 1)" in
"if pc, ok := a.sessionCancels[sessionID]; ok && !(hasSeq && pc.hasWatermark && pc.watermark >= seq) {") CancelSkipsLaterPrompt=TRUE ;;
"if pc, ok := a.sessionCancels[sessionID]; ok {") CancelSkipsLaterPrompt=FALSE ;;
*) drift "cancelPrompt guards its cancel in a new form" ;;
esac

# RegisterKeepsNewer: registerPrompt keeps a registered prompt that arrived
# later, and cancels the new one at once.
l_keep=$(line_of "$B_reg" "if hasWatermark && prev.hasWatermark && prev.arrival > arrival {")
if [ -n "$l_keep" ] && [ "$(line_at "$B_reg" $((l_keep + 1)))" = "cancel()" ] && [ "$(line_at "$B_reg" $((l_keep + 2)))" = "return func() {}" ]; then
	RegisterKeepsNewer=TRUE
elif ! has "$B_reg" "prev.arrival"; then
	RegisterKeepsNewer=FALSE
else
	drift "registerPrompt compares prompt arrivals in a new form"
fi

# CleanupChecksOwner: the func that registerPrompt returns deletes the entry
# only while it is still this prompt's (#83).
l_rdel=$(line_of "$B_reg" "delete(a.sessionCancels, sessionID)")
case "$(line_at "$B_reg" $((l_rdel - 1)))" in
"if a.sessionCancels[sessionID] == pc {") CleanupChecksOwner=TRUE ;;
"defer a.mu.Unlock()") CleanupChecksOwner=FALSE ;;
*) drift "the func that registerPrompt returns guards its delete in a new form" ;;
esac

# PruneDropsWhen: the guard of the first delete in pruneCancelMarkers.
l_pdel=$(line_of "$B_prune" "delete(a.sessionCancelledAt, sessionID)")
[ -n "$l_pdel" ] || drift "pruneCancelMarkers no longer deletes markers"
case "$(line_at "$B_prune" $((l_pdel - 1)))" in
"if !ok || oldest >= cancelledAt {") PruneDropsWhen=ge ;;
"if !ok || oldest > cancelledAt {") PruneDropsWhen=gt ;;
"for sessionID, cancelledAt := range a.sessionCancelledAt {") PruneDropsWhen=always ;;
*) drift "pruneCancelMarkers guards its delete in a new form" ;;
esac

# InboundStopsAtPeerWait: a request from the peer starts while the running
# notification handler waits on the peer. Both request paths count the wait.
peer_wait_count() { in_order "$1" "donePeerWait := c.startPeerWait(ctx)" "c.waitForResponse(ctx, pr, idKey)" "donePeerWait()"; }
counted=0
peer_wait_count "$B_send" && counted=$((counted + 1))
peer_wait_count "$B_sendnr" && counted=$((counted + 1))
[ "$counted" != 1 ] || drift "only one of SendRequest and SendRequestNoResult counts its peer wait (StartPeerNote, RunCancel)"
if has "$B_inb" "c.waitNotifications(ctx, watermark, true)"; then
	if [ "$counted" = 2 ] && has "$B_wait" "!(stopAtPeerWait && c.peerWaits[c.completedNotificationSeq+1] > 0)" && has "$B_peer" "c.peerWaits[seq]++"; then
		InboundStopsAtPeerWait=TRUE
	elif [ "$counted" = 0 ] && ! has "$B_wait" "c.peerWaits[" && ! has "$B_peer" "c.peerWaits[seq]++"; then
		InboundStopsAtPeerWait=FALSE
	else
		drift "the peer-wait release changed form: waitInboundBarrier passes true, but the waitNotifications loop or the startPeerWait count is new"
	fi
elif has "$B_inb" "c.waitNotifications(ctx, watermark, false)"; then
	InboundStopsAtPeerWait=FALSE
else
	drift "waitInboundBarrier calls waitNotifications in a new form"
fi

FACTS="CONSTANTS
    MarkerCancelsWhen = \"$MarkerCancelsWhen\"
    CancelRecordsMarker = $CancelRecordsMarker
    PruneDropsWhen = \"$PruneDropsWhen\"
    CancelSkipsLaterPrompt = $CancelSkipsLaterPrompt
    CleanupChecksOwner = $CleanupChecksOwner
    RegisterKeepsNewer = $RegisterKeepsNewer
    InboundStopsAtPeerWait = $InboundStopsAtPeerWait"
printf '%s\n' "$FACTS" >"$OUT/facts.cfg"
echo "Facts read from the code:"
sed -n '2,$p' "$OUT/facts.cfg"

# ---- TLC --------------------------------------------------------------------

JAVA=${JAVA:-java}
"$JAVA" -version >/dev/null 2>&1 || die "java not found: install Java 11 or newer, or set JAVA"

JAR=${TLA2TOOLS_JAR:-}
if [ -z "$JAR" ]; then
	CACHE=${ACP_TLA_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/acp-go-sdk-tla}
	JAR=$CACHE/tla2tools-$TLA_VERSION.jar
	if [ ! -f "$JAR" ]; then
		mkdir -p "$CACHE"
		echo "Downloading $TLA_URL"
		curl -fsSL -o "$JAR.tmp" "$TLA_URL" || die "download failed: $TLA_URL"
		mv "$JAR.tmp" "$JAR"
	fi
fi
echo "$TLA_SHA256  $JAR" | sha256sum -c --quiet - || die "$JAR is not tla2tools $TLA_VERSION (sha256 mismatch)"

unexpected=0
broken=0
stale=0
echo
printf '%-30s %-28s %-28s %s\n' CONFIG EXPECTED RESULT "TLC OUTPUT"
for entry in "${CONFIGS[@]}"; do
	cfg=${entry%%:*}
	want=${entry##*:}
	log=$OUT/${cfg%.cfg}.out
	work=$(mktemp -d "${TMPDIR:-/tmp}/acp-session-cancel-run.XXXXXX")
	cp "$SKILL_DIR/$SPEC.tla" "$work/"
	cat "$SKILL_DIR/$cfg" "$OUT/facts.cfg" >"$work/$cfg"
	# A non-zero TLC exit is a result, not an error: "|| got=$?" keeps the
	# ERR trap from firing.
	got=0
	(cd "$work" && "$JAVA" -XX:+UseParallelGC "-Djava.io.tmpdir=$work" "-Xmx${TLC_HEAP:-2g}" -cp "$JAR" tlc2.TLC \
		-workers 1 -cleanup -metadir "$work/meta" -config "$cfg" "$SPEC.tla") >"$log" 2>&1 || got=$?
	cp "$work/$cfg" "$OUT/${cfg%.cfg}.effective.cfg"
	rm -rf "$work"
	case $got in
	0) res=pass ;;
	11) res=NoDeadlock ;;
	12) res=$(sed -n 's/^Error: Invariant \(.*\) is violated.*/\1/p' "$log" | head -n 1) ;;
	*)
		res="error($got)"
		broken=1
		;;
	esac
	printf '%-30s %-28s %-28s %s\n' "$cfg" "$want" "$res" "$log"
	if [ "$res" != "$want" ] && [ "$broken" = 0 ]; then
		unexpected=$((unexpected + 1))
		if [ "$res" = pass ]; then
			echo "  $cfg passed, but the code had a known finding here. Read \"A finding is gone\" in SKILL.md." >&2
		fi
	fi
done
echo "${#CONFIGS[@]} configs: $((${#CONFIGS[@]} - unexpected)) as expected, $unexpected unexpected"

# ---- Is the covered code still the code that was synced? -------------------

echo
if [ ! -f "$SKILL_DIR/fingerprint.txt" ]; then
	echo "No fingerprint.txt. Run check.sh --update-fingerprint after you review the mapping."
	stale=1
elif ! diff <(fingerprint) "$SKILL_DIR/fingerprint.txt" >"$OUT/fingerprint.diff"; then
	echo "STALE: this covered code changed since the sync commit:"
	grep '^<' "$OUT/fingerprint.diff" | sed 's/^< [0-9a-f]*  /  /'
	echo "Review the mapping in SKILL.md before you trust the result."
	stale=1
else
	echo "Covered code matches fingerprint.txt."
fi

echo "TLC output kept in $OUT"
if [ "$broken" = 1 ]; then
	echo "TLC did not finish on a config. Read its output: this is a model or setup error." >&2
	exit 2
fi
if [ "$unexpected" -gt 0 ]; then exit 1; fi
if [ "$stale" = 1 ]; then exit 3; fi
exit 0
