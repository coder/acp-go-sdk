#!/usr/bin/env bash
# Model-check the notification barrier in connection.go with TLC.
#
#   check.sh [--deep] [--out DIR]   run the checks, keep TLC output in DIR
#   check.sh --update-fingerprint   record the covered code as synced
#
# Exit codes:
#   0  every config gave its expected result and the covered code matches
#      fingerprint.txt
#   1  a property failed that should hold, or a config that should fail
#      passed: read the TLC output named in the summary
#   2  setup or drift error: a tool is missing, or check.sh cannot find a
#      line of connection.go that the model depends on
#   3  every result was as expected, but covered functions changed since
#      the sync commit: review the mapping in SKILL.md
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
SRC=$REPO/connection.go
SPEC=NotificationBarrier

# TLC 1.7.4 is the last immutable TLA+ release.
TLA_VERSION=1.7.4
TLA_SHA256=936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88
TLA_URL=https://github.com/tlaplus/tlaplus/releases/download/v$TLA_VERSION/tla2tools.jar

# config:expected TLC exit code. 0 = pass, 13 = liveness violation.
CONFIGS=(barrier.cfg:0 slow-handler.cfg:0 drain-timeout.cfg:0 nested-handler.cfg:0
	inbound-request.cfg:0 inbound-slow-handler.cfg:0 inbound-nested.cfg:0 inbound-nested-peer.cfg:0)
DEEP_CONFIGS=(deep-two-callers.cfg:0)

COVERED=(
	"func NewConnection("
	"func (c *Connection) receive("
	"func (c *Connection) handleCancelRequest("
	"func (c *Connection) shutdownReceive("
	"func (c *Connection) processNotifications("
	"func (c *Connection) handleResponse("
	"func SendRequest[T any](c *Connection,"
	"func (c *Connection) SendRequestNoResult("
	"func (c *Connection) waitForResponse("
	"func (c *Connection) waitNotificationsUpTo("
	"func (c *Connection) waitNotifications("
	"func (c *Connection) startPeerWait("
	"func (c *Connection) waitInboundBarrier("
	"func (c *Connection) notificationSeq("
	"func (c *Connection) handleInbound("
	"func (c *Connection) sendInboundRequestError("
	"func (c *Connection) cleanupPending("
	"func (c *Connection) Done("
	"func (c *Connection) waitForNotificationDrain("
)

die() {
	echo "check.sh: $*" >&2
	exit 2
}

drift() {
	echo "DRIFT: $*" >&2
	echo "The model no longer matches connection.go. Re-read the mapping in SKILL.md." >&2
	exit 2
}

# body SIGNATURE: print a top-level Go func from its signature line to its
# closing brace (gofmt puts it alone in column 1).
body() {
	SIG="$1" awk 'index($0, ENVIRON["SIG"]) == 1 { on = 1 } on { print } on && /^}$/ { exit }' "$SRC"
}

need_body() {
	local b
	b=$(body "$1")
	[ -n "$b" ] || drift "function not found: $1"
	printf '%s\n' "$b"
}

has() { grep -Fq -- "$2" <<<"$1"; }
line_of() { { grep -Fn -- "$2" <<<"$1" || true; } | head -n 1 | cut -d: -f1; }
count() { grep -Fc -- "$2" <<<"$1" || true; }

fingerprint() {
	local sig
	for sig in "${COVERED[@]}"; do
		printf '%s  %s\n' "$(need_body "$sig" | sha256sum | cut -d' ' -f1)" "$sig"
	done
}

[ -f "$SRC" ] || die "missing $SRC"

if [ "${1:-}" = "--update-fingerprint" ]; then
	fingerprint >"$SKILL_DIR/fingerprint.txt"
	echo "wrote $SKILL_DIR/fingerprint.txt"
	echo "Commit it with the model change. That commit becomes the sync commit."
	exit 0
fi

DEEP=0
OUT=""
while [ $# -gt 0 ]; do
	case "$1" in
	--deep) DEEP=1 ;;
	--out)
		shift
		OUT=${1:?--out needs a directory}
		;;
	*) die "unknown argument: $1" ;;
	esac
	shift
done
[ -n "$OUT" ] || OUT=$(mktemp -d "${TMPDIR:-/tmp}/acp-barrier-tlc.XXXXXX")
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)

# ---- Read the facts that the model depends on from connection.go ----------

B_new=$(need_body "func NewConnection(")
B_recv=$(need_body "func (c *Connection) receive(")
B_shut=$(need_body "func (c *Connection) shutdownReceive(")
B_proc=$(need_body "func (c *Connection) processNotifications(")
B_resp=$(need_body "func (c *Connection) handleResponse(")
B_send=$(need_body "func SendRequest[T any](c *Connection,")
B_sendnr=$(need_body "func (c *Connection) SendRequestNoResult(")
B_upto=$(need_body "func (c *Connection) waitNotificationsUpTo(")
B_wait=$(need_body "func (c *Connection) waitNotifications(")
B_peer=$(need_body "func (c *Connection) startPeerWait(")
B_inb=$(need_body "func (c *Connection) waitInboundBarrier(")
B_drain=$(need_body "func (c *Connection) waitForNotificationDrain(")

# Shape the model assumes. A change here needs a new model, not a flag.
[ "$(count "$B_new" "go c.receive()")" = 1 ] || drift "NewConnection must start receive() once"
[ "$(count "$B_new" "go c.processNotifications()")" = 1 ] ||
	drift "NewConnection must start one processNotifications() worker"
# Handlers get c.inboundCtx, or handlerCtx, which adds the notification to it.
HANDLER_CTX="handlerCtx := context.WithValue(c.inboundCtx, handlingNotificationKey{}, handlingNotification{conn: c, seq: queued.seq})"
if has "$B_proc" "c.handleInbound(handlerCtx, queued.msg)" && has "$B_proc" "$HANDLER_CTX"; then
	HANDLE_CALL="c.handleInbound(handlerCtx, queued.msg)"
elif has "$B_proc" "c.handleInbound(c.inboundCtx, queued.msg)"; then
	HANDLE_CALL="c.handleInbound(c.inboundCtx, queued.msg)"
else
	drift "processNotifications no longer calls handleInbound with c.inboundCtx or handlerCtx"
fi
! has "$B_proc" "go c.handleInbound" || drift "processNotifications runs handlers concurrently"
has "$B_recv" "c.handleResponse(&msg)" || drift "receive() no longer calls handleResponse(&msg)"
! has "$B_recv" "go c.handleResponse" || drift "receive() handles responses concurrently"
has "$B_recv" "case c.notificationQueue <- queuedNotification{seq: seq, msg: &m}:" ||
	drift "receive() no longer queues notifications with a non-blocking send"
has "$B_recv" "c.lastEnqueuedNotificationSeq++" || drift "receive() no longer numbers notifications"

has "$B_upto" "return c.waitNotifications(ctx, target, false)" ||
	drift "waitNotificationsUpTo is no longer waitNotifications(ctx, target, false)"

# The wait loop, with or without the release while the running handler waits
# on the peer.
FOR_PLAIN="for c.completedNotificationSeq < target {"
FOR_PEER="for c.completedNotificationSeq < target && !(stopAtPeerWait && c.peerWaits[c.completedNotificationSeq+1] > 0) {"
if has "$B_wait" "$FOR_PEER"; then
	FOR_WAIT=$FOR_PEER
	LoopStopsAtPeerWait=TRUE
elif has "$B_wait" "$FOR_PLAIN"; then
	FOR_WAIT=$FOR_PLAIN
	LoopStopsAtPeerWait=FALSE
else
	drift "waitNotifications no longer loops on completedNotificationSeq < target"
fi

WAIT_CALL="if err := c.waitNotificationsUpTo(ctx, resp.notificationWatermark); err != nil {"
barrier_fact() {
	if has "$1" "$WAIT_CALL"; then
		echo TRUE
	elif has "$1" "waitNotificationsUpTo"; then
		drift "$2 calls waitNotificationsUpTo in a new form"
	else
		echo FALSE
	fi
}
f1=$(barrier_fact "$B_send" SendRequest)
f2=$(barrier_fact "$B_sendnr" SendRequestNoResult)
if [ "$f1" = TRUE ] && [ "$f2" = TRUE ]; then WaitsForBarrier=TRUE; else WaitsForBarrier=FALSE; fi

if has "$B_resp" "watermark := c.lastEnqueuedNotificationSeq"; then
	WatermarkSource='"enqueued"'
elif has "$B_resp" "watermark := c.completedNotificationSeq"; then
	WatermarkSource='"completed"'
else
	drift "handleResponse sets the watermark in a new way"
fi

l_handle=$(line_of "$B_proc" "$HANDLE_CALL")
l_complete=$(line_of "$B_proc" "c.completedNotificationSeq = queued.seq")
[ -n "$l_complete" ] || drift "processNotifications no longer sets completedNotificationSeq = queued.seq"
if [ "$l_handle" -lt "$l_complete" ]; then CompletesAfterHandler=TRUE; else CompletesAfterHandler=FALSE; fi

l_bc=$(line_of "$B_proc" "c.notifyCond.Broadcast()")
if [ -n "$l_bc" ] && [ "$l_bc" -gt "$l_complete" ]; then WorkerBroadcasts=TRUE; else WorkerBroadcasts=FALSE; fi

# A broadcaster goroutine started before the wait loop.
waker_fact() {
	local go_l bc_l for_l
	for_l=$(line_of "$1" "$3")
	[ -n "$for_l" ] || drift "$2 no longer loops on completedNotificationSeq < target"
	go_l=$(line_of "$1" "go func() {")
	bc_l=$(line_of "$1" "c.notifyCond.Broadcast()")
	if [ -n "$go_l" ] && [ -n "$bc_l" ] && [ "$go_l" -lt "$bc_l" ] && [ "$bc_l" -lt "$for_l" ]; then
		echo TRUE
	else
		echo FALSE
	fi
}
CtxWakerSpawned=$(waker_fact "$B_wait" waitNotifications "$FOR_WAIT")
DrainTimerWaker=$(waker_fact "$B_drain" waitForNotificationDrain "$FOR_PLAIN")

l_done=$(line_of "$B_new" "<-c.ctx.Done()")
l_newbc=$(line_of "$B_new" "c.notifyCond.Broadcast()")
if [ -n "$l_done" ] && [ -n "$l_newbc" ] && [ "$l_done" -lt "$l_newbc" ]; then
	ConnDoneWaker=TRUE
else
	ConnDoneWaker=FALSE
fi

l_cancel=$(line_of "$B_shut" "c.inboundCancel(cause)")
[ -n "$l_cancel" ] || drift "shutdownReceive no longer calls c.inboundCancel(cause)"
l_drain=$(line_of "$B_shut" "c.waitForNotificationDrain(finalEnqueuedSeq, notificationQueueDrainTimeout)")
if [ -n "$l_drain" ] && [ "$l_drain" -lt "$l_cancel" ]; then
	DrainBeforeInboundCancel=TRUE
else
	DrainBeforeInboundCancel=FALSE
fi

if has "$B_recv" "c.lastEnqueuedNotificationSeq--"; then RollsBackOnOverflow=TRUE; else RollsBackOnOverflow=FALSE; fi

# An inbound request goroutine waits for the notifications that receive()
# had queued when the request arrived.
# With stopAtPeerWait, it also starts while the running notification
# handler waits on the peer. waitInboundBarrier then turns an ended ctx into
# the cancel error.
IN_BARRIER="if err := c.waitInboundBarrier(reqCtx, watermark); err != nil {"
INB_WAIT="if err := c.waitNotifications(ctx, watermark, true); err != nil {"
INB_WAIT_FULL="if err := c.waitNotifications(ctx, watermark, false); err != nil {"
INB_CAUSE="if cause := context.Cause(ctx); cause != nil {"
PostWaitChecksCtx=FALSE
if has "$B_recv" "$IN_BARRIER"; then
	InboundWaits=TRUE
	if has "$B_inb" "$INB_WAIT"; then
		in_stop=TRUE
		l_iw=$(line_of "$B_inb" "$INB_WAIT")
	elif has "$B_inb" "$INB_WAIT_FULL"; then
		in_stop=FALSE
		l_iw=$(line_of "$B_inb" "$INB_WAIT_FULL")
	else
		drift "waitInboundBarrier calls waitNotifications in a new form"
	fi
	l_cause=$(line_of "$B_inb" "$INB_CAUSE")
	if [ -n "$l_cause" ] && [ "$l_cause" -gt "$l_iw" ]; then PostWaitChecksCtx=TRUE; fi
elif has "$B_recv" "if err := c.waitNotifications(reqCtx, watermark, true); err != nil {"; then
	InboundWaits=TRUE
	in_stop=TRUE
elif has "$B_recv" "if err := c.waitNotifications(reqCtx, watermark, false); err != nil {" ||
	has "$B_recv" "if err := c.waitNotificationsUpTo(reqCtx, watermark); err != nil {"; then
	InboundWaits=TRUE
	in_stop=FALSE
elif has "$B_recv" "waitNotifications" || has "$B_recv" "waitInboundBarrier"; then
	drift "receive() waits for earlier notifications in a new form"
else
	InboundWaits=FALSE
	in_stop=FALSE
fi
if [ "$InboundWaits" = TRUE ]; then
	has "$B_recv" "watermark := c.lastEnqueuedNotificationSeq" ||
		drift "receive() waits for a watermark that it no longer reads from lastEnqueuedNotificationSeq"
fi
if [ "$in_stop" = TRUE ] && [ "$LoopStopsAtPeerWait" = TRUE ]; then
	InboundStopsAtPeerWait=TRUE
else
	InboundStopsAtPeerWait=FALSE
fi

# startPeerWait around waitForResponse, in both request functions.
has "$B_peer" "seq, ok := c.notificationSeq(ctx)" ||
	drift "startPeerWait no longer finds the running handler with notificationSeq(ctx)"
peer_call_fact() {
	local start_l wfr_l done_l starts=FALSE ends=FALSE
	start_l=$(line_of "$1" "donePeerWait := c.startPeerWait(ctx)")
	wfr_l=$(line_of "$1" "resp, err := c.waitForResponse(ctx, pr, idKey)")
	done_l=$(line_of "$1" "donePeerWait()")
	[ -n "$wfr_l" ] || drift "$2 no longer calls waitForResponse(ctx, pr, idKey)"
	if [ -n "$start_l" ] && [ "$start_l" -lt "$wfr_l" ]; then starts=TRUE; fi
	if [ -n "$done_l" ] && [ "$done_l" -gt "$wfr_l" ]; then ends=TRUE; fi
	echo "$starts $ends"
}
read -r s1 e1 <<<"$(peer_call_fact "$B_send" SendRequest)"
read -r s2 e2 <<<"$(peer_call_fact "$B_sendnr" SendRequestNoResult)"
l_inc=$(line_of "$B_peer" "c.peerWaits[seq]++")
l_pbc=$(line_of "$B_peer" "c.notifyCond.Broadcast()")
if [ -n "$l_inc" ] && [ "$s1" = TRUE ] && [ "$s2" = TRUE ]; then PeerWaitCounts=TRUE; else PeerWaitCounts=FALSE; fi
if [ -n "$l_pbc" ] && { [ -z "$l_inc" ] || [ "$l_pbc" -gt "$l_inc" ]; }; then PeerWaitBroadcasts=TRUE; else PeerWaitBroadcasts=FALSE; fi
if has "$B_peer" "if c.peerWaits[seq]--; c.peerWaits[seq] == 0 {" && [ "$e1" = TRUE ] && [ "$e2" = TRUE ]; then
	PeerWaitEnds=TRUE
else
	PeerWaitEnds=FALSE
fi

# loop_of BODY: the lines from the wait loop to the end of the function.
loop_of() { sed -n "/for c.completedNotificationSeq < target/,\$p" <<<"$1"; }
L_wait=$(loop_of "$B_wait")
l_cdone=$(line_of "$L_wait" "case <-c.Done():")
l_cctx=$(line_of "$L_wait" "case <-ctx.Done():")
[ -n "$l_cctx" ] || drift "the waitNotifications loop no longer selects on ctx.Done()"
if [ -n "$l_cdone" ] && [ "$l_cdone" -lt "$l_cctx" ]; then WaitChecksDisconnect=TRUE; else WaitChecksDisconnect=FALSE; fi
L_drain=$(loop_of "$B_drain")
l_err=$(line_of "$L_drain" "if ctx.Err() != nil {")
l_dwait=$(line_of "$L_drain" "c.notifyCond.Wait()")
[ -n "$l_dwait" ] || drift "the waitForNotificationDrain loop no longer waits on notifyCond"
if [ -n "$l_err" ] && [ "$l_err" -lt "$l_dwait" ]; then DrainChecksTimeout=TRUE; else DrainChecksTimeout=FALSE; fi

# The cap needs both halves: the handler ctx names the notification, and the
# wait lowers its target for that ctx before the loop.
CAP_IF="if h, ok := ctx.Value(handlingNotificationKey{}).(handlingNotification); ok && h.conn == c && c.completedNotificationSeq < h.seq {"
l_capif=$(line_of "$B_wait" "$CAP_IF")
l_cap=$(line_of "$B_wait" "target = min(target, h.seq-1)")
l_for=$(line_of "$B_wait" "$FOR_WAIT")
if [ -n "$l_capif" ] && [ -n "$l_cap" ] && [ "$l_cap" = $((l_capif + 1)) ] && [ "$l_cap" -lt "$l_for" ]; then
	caps=TRUE
elif has "$B_wait" "handlingNotification"; then
	drift "waitNotifications reads the handler ctx in a new form"
else
	caps=FALSE
fi
if [ "$caps" = TRUE ] && [ "$HANDLE_CALL" = "c.handleInbound(handlerCtx, queued.msg)" ]; then
	HandlerCtxCapsWait=TRUE
else
	HandlerCtxCapsWait=FALSE
fi

FACTS="CONSTANTS
    WaitsForBarrier = $WaitsForBarrier
    WatermarkSource = $WatermarkSource
    CompletesAfterHandler = $CompletesAfterHandler
    WorkerBroadcasts = $WorkerBroadcasts
    CtxWakerSpawned = $CtxWakerSpawned
    ConnDoneWaker = $ConnDoneWaker
    DrainBeforeInboundCancel = $DrainBeforeInboundCancel
    DrainTimerWaker = $DrainTimerWaker
    RollsBackOnOverflow = $RollsBackOnOverflow
    WaitChecksDisconnect = $WaitChecksDisconnect
    DrainChecksTimeout = $DrainChecksTimeout
    HandlerCtxCapsWait = $HandlerCtxCapsWait
    InboundWaits = $InboundWaits
    InboundStopsAtPeerWait = $InboundStopsAtPeerWait
    PeerWaitCounts = $PeerWaitCounts
    PeerWaitBroadcasts = $PeerWaitBroadcasts
    PeerWaitEnds = $PeerWaitEnds
    PostWaitChecksCtx = $PostWaitChecksCtx"
printf '%s\n' "$FACTS" >"$OUT/facts.cfg"
echo "Facts read from connection.go:"
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

run_list=("${CONFIGS[@]}")
[ "$DEEP" = 1 ] && run_list+=("${DEEP_CONFIGS[@]}")

failed=0
broken=0
stale=0
echo
printf '%-24s %-10s %-9s %s\n' CONFIG EXPECTED RESULT "TLC OUTPUT"
for entry in "${run_list[@]}"; do
	cfg=${entry%%:*}
	want=${entry##*:}
	work=$(mktemp -d "${TMPDIR:-/tmp}/acp-barrier-run.XXXXXX")
	cp "$SKILL_DIR/$SPEC.tla" "$work/"
	cat "$SKILL_DIR/$cfg" "$OUT/facts.cfg" >"$work/$cfg"
	# A non-zero TLC exit is a result, not an error: "|| got=$?" keeps the
	# ERR trap from firing.
	got=0
	(cd "$work" && "$JAVA" -XX:+UseParallelGC "-Xmx${TLC_HEAP:-4g}" -cp "$JAR" tlc2.TLC \
		-workers 1 -cleanup -metadir "$work/meta" -config "$cfg" "$SPEC.tla") >"$OUT/${cfg%.cfg}.out" 2>&1 || got=$?
	cp "$work/$cfg" "$OUT/${cfg%.cfg}.effective.cfg"
	rm -rf "$work"
	case $got in
	0) res=pass ;;
	11) res=deadlock ;;
	12) res=invariant ;;
	13) res=liveness ;;
	*) res="error($got)" ;;
	esac
	case $want in
	0) exp=pass ;;
	13) exp=liveness ;;
	esac
	printf '%-24s %-10s %-9s %s\n' "$cfg" "$exp" "$res" "$OUT/${cfg%.cfg}.out"
	case $got in
	11) echo "  rule: NoDeadlock" ;;
	12) sed -n 's/^Error: Invariant \(.*\) is violated.*/  rule: \1/p' "$OUT/${cfg%.cfg}.out" | head -n 1 ;;
	13) echo "  rule: one of the PROPERTIES in $cfg (TLC does not say which; /statechart names it)" ;;
	esac
	if [ "$got" != "$want" ]; then
		case $got in
		0)
			echo "  $cfg passed, but it should fail. Update SKILL.md and its expected result." >&2
			failed=1
			;;
		11 | 12 | 13) failed=1 ;;
		*)
			echo "  TLC did not finish. Read the output: this is a model or setup error." >&2
			broken=1
			;;
		esac
	fi
done

# ---- Is the covered code still the code that was synced? -------------------

echo
if [ ! -f "$SKILL_DIR/fingerprint.txt" ]; then
	echo "No fingerprint.txt. Run check.sh --update-fingerprint after you review the mapping."
	stale=1
elif ! diff <(fingerprint) "$SKILL_DIR/fingerprint.txt" >"$OUT/fingerprint.diff"; then
	echo "STALE: these covered functions changed since the sync commit:"
	grep '^<' "$OUT/fingerprint.diff" | sed 's/^< [0-9a-f]*  /  /'
	echo "Review the mapping in SKILL.md before you trust a pass."
	stale=1
else
	echo "Covered functions match fingerprint.txt."
fi

echo "TLC output kept in $OUT"
if [ "$broken" = 1 ]; then exit 2; fi
if [ "$failed" = 1 ]; then exit 1; fi
if [ "$stale" = 1 ]; then exit 3; fi
exit 0
