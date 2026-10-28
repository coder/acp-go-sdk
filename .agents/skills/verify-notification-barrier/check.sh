#!/usr/bin/env bash
# Model-check the notification barrier in connection.go with TLC.
#
#   check.sh [--deep] [--out DIR]   run the checks, keep TLC output in DIR
#   check.sh --update-fingerprint   record the covered code as synced
#
# Exit codes:
#   0  every config gave its expected result and the covered code matches
#      fingerprint.txt
#   1  a property failed that should hold, or the known finding no longer
#      reproduces: read the TLC output named in the summary
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
CONFIGS=(barrier.cfg:0 slow-handler.cfg:0 drain-timeout.cfg:0 nested-handler.cfg:13)
DEEP_CONFIGS=(deep-two-callers.cfg:0)

COVERED=(
	"func NewConnection("
	"func (c *Connection) receive("
	"func (c *Connection) shutdownReceive("
	"func (c *Connection) processNotifications("
	"func (c *Connection) handleResponse("
	"func SendRequest[T any](c *Connection,"
	"func (c *Connection) SendRequestNoResult("
	"func (c *Connection) waitForResponse("
	"func (c *Connection) waitNotificationsUpTo("
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
	echo "Now set the sync commit in SKILL.md to: $(git -C "$REPO" rev-parse HEAD)"
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
B_wait=$(need_body "func (c *Connection) waitNotificationsUpTo(")
B_drain=$(need_body "func (c *Connection) waitForNotificationDrain(")

# Shape the model assumes. A change here needs a new model, not a flag.
[ "$(count "$B_new" "go c.receive()")" = 1 ] || drift "NewConnection must start receive() once"
[ "$(count "$B_new" "go c.processNotifications()")" = 1 ] ||
	drift "NewConnection must start one processNotifications() worker"
has "$B_proc" "c.handleInbound(c.inboundCtx, queued.msg)" ||
	drift "processNotifications no longer calls handleInbound(c.inboundCtx, queued.msg)"
! has "$B_proc" "go c.handleInbound" || drift "processNotifications runs handlers concurrently"
has "$B_recv" "c.handleResponse(&msg)" || drift "receive() no longer calls handleResponse(&msg)"
! has "$B_recv" "go c.handleResponse" || drift "receive() handles responses concurrently"
has "$B_recv" "case c.notificationQueue <- queuedNotification{seq: seq, msg: &m}:" ||
	drift "receive() no longer queues notifications with a non-blocking send"
has "$B_recv" "c.lastEnqueuedNotificationSeq++" || drift "receive() no longer numbers notifications"

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

l_handle=$(line_of "$B_proc" "c.handleInbound(c.inboundCtx, queued.msg)")
l_complete=$(line_of "$B_proc" "c.completedNotificationSeq = queued.seq")
[ -n "$l_complete" ] || drift "processNotifications no longer sets completedNotificationSeq = queued.seq"
if [ "$l_handle" -lt "$l_complete" ]; then CompletesAfterHandler=TRUE; else CompletesAfterHandler=FALSE; fi

l_bc=$(line_of "$B_proc" "c.notifyCond.Broadcast()")
if [ -n "$l_bc" ] && [ "$l_bc" -gt "$l_complete" ]; then WorkerBroadcasts=TRUE; else WorkerBroadcasts=FALSE; fi

# A broadcaster goroutine started before the wait loop.
waker_fact() {
	local go_l bc_l for_l
	for_l=$(line_of "$1" "for c.completedNotificationSeq < target {")
	[ -n "$for_l" ] || drift "$2 no longer loops on completedNotificationSeq < target"
	go_l=$(line_of "$1" "go func() {")
	bc_l=$(line_of "$1" "c.notifyCond.Broadcast()")
	if [ -n "$go_l" ] && [ -n "$bc_l" ] && [ "$go_l" -lt "$bc_l" ] && [ "$bc_l" -lt "$for_l" ]; then
		echo TRUE
	else
		echo FALSE
	fi
}
CtxWakerSpawned=$(waker_fact "$B_wait" waitNotificationsUpTo)
DrainTimerWaker=$(waker_fact "$B_drain" waitForNotificationDrain)

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

# loop_of BODY: the lines from the wait loop to the end of the function.
loop_of() { sed -n "/for c.completedNotificationSeq < target {/,\$p" <<<"$1"; }
L_wait=$(loop_of "$B_wait")
l_cdone=$(line_of "$L_wait" "case <-c.Done():")
l_cctx=$(line_of "$L_wait" "case <-ctx.Done():")
[ -n "$l_cctx" ] || drift "the waitNotificationsUpTo loop no longer selects on ctx.Done()"
if [ -n "$l_cdone" ] && [ "$l_cdone" -lt "$l_cctx" ]; then WaitChecksDisconnect=TRUE; else WaitChecksDisconnect=FALSE; fi
L_drain=$(loop_of "$B_drain")
l_err=$(line_of "$L_drain" "if ctx.Err() != nil {")
l_dwait=$(line_of "$L_drain" "c.notifyCond.Wait()")
[ -n "$l_dwait" ] || drift "the waitForNotificationDrain loop no longer waits on notifyCond"
if [ -n "$l_err" ] && [ "$l_err" -lt "$l_dwait" ]; then DrainChecksTimeout=TRUE; else DrainChecksTimeout=FALSE; fi

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
    DrainChecksTimeout = $DrainChecksTimeout"
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
			echo "  $cfg passed, but it should reproduce the known finding. Update SKILL.md." >&2
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
