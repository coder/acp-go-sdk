------------------------- MODULE NotificationBarrier -------------------------
(***************************************************************************)
(* A hand model of the response-scoped notification barrier in            *)
(* connection.go. Each action names the Go code it stands for. One action *)
(* is one indivisible step in the code: a critical section under          *)
(* notifyMu, a channel operation, or one select.                          *)
(*                                                                         *)
(* The constants in the second group are facts about connection.go.       *)
(* check.sh reads them from the source on every run, so a change to one   *)
(* of those lines changes the model.                                      *)
(***************************************************************************)
EXTENDS Integers, Sequences, FiniteSets

CONSTANTS
    MaxNotes,           \* notifications the peer can send
    QueueCap,           \* cap(c.notificationQueue), defaultMaxQueuedNotifications
    NoteKinds,          \* handler kinds: "plain", "slow", "nested"
    AppCallers,         \* app goroutines that call SendRequest, such as Prompt()
    PeerCanClose,       \* the peer can close its stream (EOF)
    CtxCanCancel,       \* the app can cancel a caller's ctx
    DrainTimeoutFires,  \* the 5 s notificationQueueDrainTimeout can fire
    InReqs,             \* requests the peer sends us, each run in its own goroutine
    PeerCanCancelReq,   \* the peer can send $/cancel_request for an inbound request
    PeerNeedsInboundAnswer, \* the peer answers the nested request only after every inbound request is answered
    \* Facts read from connection.go by check.sh:
    WaitsForBarrier,          \* SendRequest and SendRequestNoResult call waitNotificationsUpTo
    WatermarkSource,          \* "enqueued" or "completed": the counter handleResponse records
    CompletesAfterHandler,    \* processNotifications counts a notification after handleInbound returns
    WorkerBroadcasts,         \* processNotifications calls notifyCond.Broadcast after counting
    CtxWakerSpawned,          \* waitNotificationsUpTo starts the ctx.Done broadcaster
    ConnDoneWaker,            \* NewConnection starts the c.ctx.Done broadcaster
    DrainBeforeInboundCancel, \* shutdownReceive drains before inboundCancel
    DrainTimerWaker,          \* waitForNotificationDrain starts the timeout broadcaster
    RollsBackOnOverflow,      \* receive decrements lastEnqueuedNotificationSeq on overflow
    WaitChecksDisconnect,     \* the waitNotificationsUpTo loop checks c.Done() first
    DrainChecksTimeout,       \* the waitForNotificationDrain loop checks ctx.Err()
    HandlerCtxCapsWait,       \* waitNotificationsUpTo caps the target for a running handler's ctx
    InboundWaits              \* an inbound request goroutine calls waitNotificationsUpTo(reqCtx, watermark)

ASSUME WatermarkSource \in {"enqueued", "completed"}
ASSUME NoteKinds \subseteq {"plain", "slow", "nested"}

Nested  == "nested"   \* the request made from inside a "nested" handler
Drainer == "drainer"  \* the drain goroutine as a cond waiter
None    == "none"
Empty   == -1         \* an empty pendingResponse.ch

Reqs == AppCallers \cup (IF "nested" \in NoteKinds THEN {Nested} ELSE {}) \cup InReqs

ASSUME InReqs \cap (AppCallers \cup {Nested, Drainer, None}) = {}

VARIABLES
    \* the peer and the wire (peer to us, in order)
    wire, notesSent, nestedSent, respSent, eofSent, sentReq,
    \* receive()
    recvPc, recvTmp,
    \* Connection fields
    lastEnq, completed, queue, qClosed, final,
    connDone, inboundDone, lock, waitSet,
    \* processNotifications()
    workerPc, cur,
    \* SendRequest callers
    callerPc, pending, ch, resp, ctxCanceled, waker,
    \* goroutines that broadcast on notifyCond
    connWatcher, drainWaker,
    \* the goroutine that shutdownReceive starts
    drainPc, timerFired,
    \* history for the properties (not in the code)
    received, handled, readBefore, respRead, result, finalIds, nestedFrom,
    \* inbound requests: the peer's side, c.inflight, the request goroutine,
    \* and how its handler started (history)
    inSent, cancelSent, inflight, inPc, inStart

vars == <<wire, notesSent, nestedSent, respSent, eofSent, sentReq,
          recvPc, recvTmp, lastEnq, completed, queue, qClosed, final,
          connDone, inboundDone, lock, waitSet, workerPc, cur,
          callerPc, pending, ch, resp, ctxCanceled, waker,
          connWatcher, drainWaker, drainPc, timerFired,
          received, handled, readBefore, respRead, result, finalIds, nestedFrom,
          inSent, cancelSent, inflight, inPc, inStart>>

inVars == <<inSent, cancelSent, inflight, inPc, inStart>>

peerVars   == <<wire, notesSent, nestedSent, respSent, eofSent>>
historyVars == <<received, handled, readBefore, respRead, result, finalIds, nestedFrom>>

Range(s) == {s[i] : i \in DOMAIN s}
HandledSet == Range(handled)

\* The ctx that a caller passes. A handler gets c.inboundCtx. An inbound
\* request's reqCtx is context.WithCancelCause(c.ctx): $/cancel_request or a
\* disconnect ends it.
CtxDone(r) == IF r = Nested THEN inboundDone
              ELSE IF r \in InReqs THEN ctxCanceled[r] \/ connDone
              ELSE ctxCanceled[r]

\* notifyCond.Broadcast(): every waiter wakes and must take notifyMu again.
Broadcast ==
    /\ callerPc' = [r \in Reqs |-> IF r \in waitSet THEN "relock" ELSE callerPc[r]]
    /\ drainPc' = IF Drainer \in waitSet THEN "relock" ELSE drainPc
    /\ waitSet' = {}

Msg(t, id, kind, r) == [t |-> t, id |-> id, kind |-> kind, r |-> r]

Init ==
    /\ wire = <<>> /\ notesSent = 0 /\ nestedSent = FALSE
    /\ respSent = {} /\ eofSent = FALSE /\ sentReq = {}
    /\ recvPc = "read" /\ recvTmp = [r |-> None, w |-> 0]
    /\ lastEnq = 0 /\ completed = 0 /\ queue = <<>> /\ qClosed = FALSE /\ final = 0
    /\ connDone = FALSE /\ inboundDone = FALSE /\ lock = None /\ waitSet = {}
    /\ workerPc = "dequeue" /\ cur = [seq |-> 0, id |-> 0, kind |-> "plain"]
    /\ callerPc = [r \in Reqs |-> "idle"] /\ pending = {}
    /\ ch = [r \in Reqs |-> Empty] /\ resp = [r \in Reqs |-> 0]
    /\ ctxCanceled = [r \in Reqs |-> FALSE] /\ waker = [r \in Reqs |-> "off"]
    /\ connWatcher = IF ConnDoneWaker THEN "armed" ELSE "absent"
    /\ drainWaker = "off" /\ drainPc = "off" /\ timerFired = FALSE
    /\ received = <<>> /\ handled = <<>>
    /\ readBefore = [r \in Reqs |-> {}] /\ respRead = [r \in Reqs |-> FALSE]
    /\ result = [r \in Reqs |-> "none"] /\ finalIds = {} /\ nestedFrom = 0
    /\ inSent = {} /\ cancelSent = {} /\ inflight = {}
    /\ inPc = [q \in InReqs |-> "none"] /\ inStart = [q \in InReqs |-> "none"]

-----------------------------------------------------------------------------
(* The peer writes to the wire. It has no fairness: it can stop at any time. *)

PeerSendsNote(k) ==
    /\ ~eofSent /\ notesSent < MaxNotes
    /\ k = Nested => ~nestedSent
    /\ notesSent' = notesSent + 1
    /\ nestedSent' = (nestedSent \/ k = Nested)
    /\ wire' = Append(wire, Msg("note", notesSent + 1, k, None))
    /\ UNCHANGED <<respSent, eofSent, sentReq, recvPc, recvTmp, lastEnq, completed,
                   queue, qClosed, final, connDone, inboundDone, lock, waitSet,
                   workerPc, cur, callerPc, pending, ch, resp, ctxCanceled, waker,
                   connWatcher, drainWaker, drainPc, timerFired, historyVars>>
    /\ UNCHANGED inVars

PeerResponds(r) ==
    /\ ~eofSent /\ r \in sentReq \ respSent
    /\ (r = Nested /\ PeerNeedsInboundAnswer) =>
          \A q \in InReqs : inPc[q] \in {"replied", "errReplied"}
    /\ respSent' = respSent \cup {r}
    /\ wire' = Append(wire, Msg("resp", 0, "none", r))
    /\ UNCHANGED <<notesSent, nestedSent, eofSent, sentReq, recvPc, recvTmp, lastEnq,
                   completed, queue, qClosed, final, connDone, inboundDone, lock,
                   waitSet, workerPc, cur, callerPc, pending, ch, resp, ctxCanceled,
                   waker, connWatcher, drainWaker, drainPc, timerFired, historyVars>>
    /\ UNCHANGED inVars

PeerCloses ==
    /\ PeerCanClose /\ ~eofSent
    /\ eofSent' = TRUE
    /\ wire' = Append(wire, Msg("eof", 0, "none", None))
    /\ UNCHANGED <<notesSent, nestedSent, respSent, sentReq, recvPc, recvTmp, lastEnq,
                   completed, queue, qClosed, final, connDone, inboundDone, lock,
                   waitSet, workerPc, cur, callerPc, pending, ch, resp, ctxCanceled,
                   waker, connWatcher, drainWaker, drainPc, timerFired, historyVars>>
    /\ UNCHANGED inVars

-----------------------------------------------------------------------------
(* receive(): one goroutine reads the wire in order. *)

\* receive: lastEnqueuedNotificationSeq++ and the non-blocking send to
\* notificationQueue, under notifyMu. On a full queue: the rollback, then
\* shutdownReceive(errNotificationQueueOverflow).
RecvNotification ==
    /\ recvPc = "read" /\ wire # <<>> /\ Head(wire).t = "note" /\ lock = None
    /\ LET m == Head(wire) IN
       IF Len(queue) < QueueCap
       THEN /\ lastEnq' = lastEnq + 1
            /\ queue' = Append(queue, [seq |-> lastEnq + 1, id |-> m.id, kind |-> m.kind])
            /\ received' = Append(received, m.id)
            /\ recvPc' = "read"
       ELSE /\ lastEnq' = IF RollsBackOnOverflow THEN lastEnq ELSE lastEnq + 1
            /\ UNCHANGED <<queue, received>>
            /\ recvPc' = "shutCancel"
    /\ wire' = Tail(wire)
    /\ UNCHANGED <<notesSent, nestedSent, respSent, eofSent, sentReq, recvTmp, completed,
                   qClosed, final, connDone, inboundDone, lock, waitSet, workerPc, cur,
                   callerPc, pending, ch, resp, ctxCanceled, waker, connWatcher,
                   drainWaker, drainPc, timerFired, handled, readBefore, respRead,
                   result, finalIds, nestedFrom>>
    /\ UNCHANGED inVars

\* handleResponse: take c.pending[id] under c.mu, then read the watermark
\* under notifyMu. A response with no pending entry is dropped.
RecvResponse ==
    /\ recvPc = "read" /\ wire # <<>> /\ Head(wire).t = "resp"
    /\ LET r == Head(wire).r IN
       IF r \in pending
       THEN /\ lock = None
            /\ pending' = pending \ {r}
            /\ recvTmp' = [r |-> r,
                           w |-> IF WatermarkSource = "enqueued" THEN lastEnq ELSE completed]
            /\ readBefore' = [readBefore EXCEPT ![r] = Range(received)]
            /\ respRead' = [respRead EXCEPT ![r] = TRUE]
            /\ recvPc' = "deliver"
       ELSE UNCHANGED <<pending, recvTmp, readBefore, respRead, recvPc>>
    /\ wire' = Tail(wire)
    /\ UNCHANGED <<notesSent, nestedSent, respSent, eofSent, sentReq, lastEnq, completed,
                   queue, qClosed, final, connDone, inboundDone, lock, waitSet, workerPc,
                   cur, callerPc, ch, resp, ctxCanceled, waker, connWatcher, drainWaker,
                   drainPc, timerFired, received, handled, result, finalIds, nestedFrom>>
    /\ UNCHANGED inVars

\* handleResponse: pr.ch <- responseEnvelope{...}. The channel has room for one.
RecvDeliver ==
    /\ recvPc = "deliver"
    /\ ch' = [ch EXCEPT ![recvTmp.r] = recvTmp.w]
    /\ recvPc' = "read"
    /\ UNCHANGED <<peerVars, sentReq, recvTmp, lastEnq, completed, queue, qClosed, final,
                   connDone, inboundDone, lock, waitSet, workerPc, cur, callerPc,
                   pending, resp, ctxCanceled, waker, connWatcher, drainWaker, drainPc,
                   timerFired, historyVars>>
    /\ UNCHANGED inVars

\* receive: scanner.Scan() returns false, then shutdownReceive(cause).
RecvEOF ==
    /\ recvPc = "read" /\ wire # <<>> /\ Head(wire).t = "eof"
    /\ wire' = Tail(wire)
    /\ recvPc' = "shutCancel"
    /\ UNCHANGED <<notesSent, nestedSent, respSent, eofSent, sentReq, recvTmp, lastEnq,
                   completed, queue, qClosed, final, connDone, inboundDone, lock,
                   waitSet, workerPc, cur, callerPc, pending, ch, resp, ctxCanceled,
                   waker, connWatcher, drainWaker, drainPc, timerFired, historyVars>>
    /\ UNCHANGED inVars

\* shutdownReceive: c.cancel(cause), so c.Done() closes.
ShutdownCancel ==
    /\ recvPc = "shutCancel"
    /\ connDone' = TRUE
    /\ recvPc' = "shutClose"
    /\ UNCHANGED <<peerVars, sentReq, recvTmp, lastEnq, completed, queue, qClosed, final,
                   inboundDone, lock, waitSet, workerPc, cur, callerPc, pending, ch,
                   resp, ctxCanceled, waker, connWatcher, drainWaker, drainPc,
                   timerFired, historyVars>>
    /\ UNCHANGED inVars

\* shutdownReceive: close(c.notificationQueue), read finalEnqueuedSeq under
\* notifyMu, start the drain goroutine. Only receive() writes
\* lastEnqueuedNotificationSeq, so one step loses no interleaving.
ShutdownClose ==
    /\ recvPc = "shutClose" /\ lock = None
    /\ qClosed' = TRUE
    /\ final' = lastEnq
    /\ finalIds' = Range(received)
    /\ drainPc' = "start"
    /\ recvPc' = "exited"
    /\ UNCHANGED <<peerVars, sentReq, recvTmp, lastEnq, completed, queue, connDone,
                   inboundDone, lock, waitSet, workerPc, cur, callerPc, pending, ch,
                   resp, ctxCanceled, waker, connWatcher, drainWaker, timerFired,
                   received, handled, readBefore, respRead, result, nestedFrom>>
    /\ UNCHANGED inVars

-----------------------------------------------------------------------------
(* processNotifications(): one goroutine handles notifications in order. *)

\* for queued := range c.notificationQueue
WorkerDequeue ==
    /\ workerPc = "dequeue" /\ queue # <<>>
    /\ cur' = Head(queue)
    /\ queue' = Tail(queue)
    /\ workerPc' = IF CompletesAfterHandler THEN "handle" ELSE "complete"
    /\ UNCHANGED <<peerVars, sentReq, recvPc, recvTmp, lastEnq, completed, qClosed, final,
                   connDone, inboundDone, lock, waitSet, callerPc, pending, ch, resp,
                   ctxCanceled, waker, connWatcher, drainWaker, drainPc, timerFired,
                   historyVars>>
    /\ UNCHANGED inVars

\* The range loop ends: the queue is closed and empty.
WorkerExit ==
    /\ workerPc = "dequeue" /\ queue = <<>> /\ qClosed
    /\ workerPc' = "exited"
    /\ UNCHANGED <<peerVars, sentReq, recvPc, recvTmp, lastEnq, completed, queue, qClosed,
                   final, connDone, inboundDone, lock, waitSet, cur, callerPc, pending,
                   ch, resp, ctxCanceled, waker, connWatcher, drainWaker, drainPc,
                   timerFired, historyVars>>
    /\ UNCHANGED inVars

AfterHandler == IF CompletesAfterHandler THEN "complete" ELSE "dequeue"

HandlerReturnsVars ==
    UNCHANGED <<peerVars, sentReq, recvPc, recvTmp, lastEnq, completed, queue, qClosed,
                final, connDone, inboundDone, lock, waitSet, cur, callerPc, pending, ch,
                resp, ctxCanceled, waker, connWatcher, drainWaker, drainPc, timerFired,
                received, readBefore, respRead, result, finalIds, nestedFrom, inVars>>

\* handleInbound returns for a handler that finishes promptly.
PlainHandlerReturns ==
    /\ workerPc = "handle" /\ cur.kind = "plain"
    /\ handled' = Append(handled, cur.id)
    /\ workerPc' = AfterHandler
    /\ HandlerReturnsVars

\* handleInbound returns for a handler that can take any time (a UI, a
\* disk write). It has no fairness.
SlowHandlerReturns ==
    /\ workerPc = "handle" /\ cur.kind = "slow"
    /\ handled' = Append(handled, cur.id)
    /\ workerPc' = AfterHandler
    /\ HandlerReturnsVars

\* A handler that calls SendRequest on the same connection with its handler
\* ctx (derived from c.inboundCtx), for example a SessionUpdate handler that
\* calls CallExtension. The call runs on this goroutine.
NestedHandlerCalls ==
    /\ workerPc = "handle" /\ cur.kind = Nested /\ callerPc[Nested] = "idle"
    /\ callerPc' = [callerPc EXCEPT ![Nested] = "registered"]
    /\ pending' = pending \cup {Nested}
    /\ workerPc' = "nestedCall"
    /\ nestedFrom' = cur.id
    /\ UNCHANGED <<peerVars, sentReq, recvPc, recvTmp, lastEnq, completed, queue, qClosed,
                   final, connDone, inboundDone, lock, waitSet, cur, ch, resp,
                   ctxCanceled, waker, connWatcher, drainWaker, drainPc, timerFired,
                   received, handled, readBefore, respRead, result, finalIds>>
    /\ UNCHANGED inVars

NestedHandlerReturns ==
    /\ workerPc = "nestedCall" /\ callerPc[Nested] = "done"
    /\ handled' = Append(handled, cur.id)
    /\ workerPc' = AfterHandler
    /\ HandlerReturnsVars

\* completedNotificationSeq = queued.seq and Broadcast, under notifyMu.
WorkerComplete ==
    /\ workerPc = "complete" /\ lock = None
    /\ completed' = cur.seq
    /\ IF WorkerBroadcasts THEN Broadcast ELSE UNCHANGED <<callerPc, drainPc, waitSet>>
    /\ workerPc' = IF CompletesAfterHandler THEN "dequeue" ELSE "handle"
    /\ UNCHANGED <<peerVars, sentReq, recvPc, recvTmp, lastEnq, queue, qClosed, final,
                   connDone, inboundDone, lock, cur, pending, ch, resp, ctxCanceled,
                   waker, connWatcher, drainWaker, timerFired, historyVars>>
    /\ UNCHANGED inVars

-----------------------------------------------------------------------------
(* SendRequest / SendRequestNoResult, one per caller r. *)

CallerVars(r) ==
    UNCHANGED <<peerVars, recvPc, recvTmp, lastEnq, completed, queue, qClosed, final,
                connDone, inboundDone, workerPc, cur, ctxCanceled, connWatcher,
                drainWaker, drainPc, timerFired, received, handled, readBefore,
                respRead, finalIds, nestedFrom, inVars>>

SetPc(r, pc) == callerPc' = [callerPc EXCEPT ![r] = pc]
SetResult(r, v) == result' = [result EXCEPT ![r] = v]

\* prepareRequest, then c.pending[idKey] = pr under c.mu. The app decides
\* when to call, so this step has no fairness.
AppCalls(r) ==
    /\ r \in AppCallers /\ callerPc[r] = "idle"
    /\ SetPc(r, "registered")
    /\ pending' = pending \cup {r}
    /\ CallerVars(r)
    /\ UNCHANGED <<sentReq, lock, waitSet, ch, resp, waker, result>>

\* c.sendMessage(msg): the request is on the wire, so the peer can answer.
SendsRequest(r) ==
    /\ callerPc[r] = "registered"
    /\ sentReq' = sentReq \cup {r}
    /\ SetPc(r, "waitResp")
    /\ CallerVars(r)
    /\ UNCHANGED <<pending, lock, waitSet, ch, resp, waker, result>>

\* waitForResponse: case resp := <-pr.ch
GetsResponse(r) ==
    /\ callerPc[r] = "waitResp" /\ ch[r] # Empty
    /\ resp' = [resp EXCEPT ![r] = ch[r]]
    /\ ch' = [ch EXCEPT ![r] = Empty]
    /\ SetPc(r, "barrier")
    /\ CallerVars(r)
    /\ UNCHANGED <<sentReq, pending, lock, waitSet, waker, result>>

\* waitForResponse: case <-ctx.Done(), then the inner select on c.Done().
SeesCtxDone(r) ==
    /\ callerPc[r] = "waitResp" /\ CtxDone(r)
    /\ SetPc(r, "ctxBranch")
    /\ CallerVars(r)
    /\ UNCHANGED <<sentReq, pending, lock, waitSet, ch, resp, waker, result>>

CtxBranchChecksDone(r) ==
    /\ callerPc[r] = "ctxBranch"
    /\ SetPc(r, IF connDone THEN "cleanupDisc" ELSE "cleanupCancel")
    /\ CallerVars(r)
    /\ UNCHANGED <<sentReq, pending, lock, waitSet, ch, resp, waker, result>>

\* waitForResponse: case <-c.Done()
SeesDisconnect(r) ==
    /\ callerPc[r] = "waitResp" /\ connDone
    /\ SetPc(r, "cleanupDisc")
    /\ CallerVars(r)
    /\ UNCHANGED <<sentReq, pending, lock, waitSet, ch, resp, waker, result>>

\* cleanupPending(idKey) and return the error. sendCancelRequest only
\* queues a $/cancel_request, so the model leaves it out.
CleansUp(r) ==
    /\ callerPc[r] \in {"cleanupDisc", "cleanupCancel"}
    /\ pending' = pending \ {r}
    /\ SetResult(r, IF callerPc[r] = "cleanupDisc" THEN "disconnected" ELSE "cancelled")
    /\ SetPc(r, "done")
    /\ CallerVars(r)
    /\ UNCHANGED <<sentReq, lock, waitSet, ch, resp, waker>>

Min(a, b) == IF a < b THEN a ELSE b

\* The target after the cap in waitNotificationsUpTo. Only the request from
\* the nested handler carries the ctx of a running handler, the one of
\* notification cur.seq.
CappedTarget(r) ==
    IF HandlerCtxCapsWait /\ r = Nested /\ completed < cur.seq
    THEN Min(resp[r], cur.seq - 1)
    ELSE resp[r]

\* waitNotificationsUpTo(ctx, watermark): target 0 returns at once.
\* Otherwise take notifyMu, cap the target, and start the ctx.Done
\* broadcaster. From here on resp[r] is the target.
EntersBarrier(r) ==
    /\ callerPc[r] = "barrier"
    /\ IF ~WaitsForBarrier \/ resp[r] = 0
       THEN /\ SetResult(r, "ok") /\ SetPc(r, "done")
            /\ UNCHANGED <<lock, waker, resp>>
       ELSE /\ lock = None /\ lock' = r
            /\ resp' = [resp EXCEPT ![r] = CappedTarget(r)]
            /\ waker' = [waker EXCEPT ![r] = IF CtxWakerSpawned THEN "armed" ELSE "off"]
            /\ SetPc(r, "loop") /\ UNCHANGED result
    /\ CallerVars(r)
    /\ UNCHANGED <<sentReq, pending, waitSet, ch>>

\* for c.completedNotificationSeq < target { ... } (holds notifyMu)
BarrierLoop(r) ==
    /\ callerPc[r] = "loop"
    /\ IF completed >= resp[r]
       THEN /\ lock' = None /\ SetResult(r, "ok") /\ SetPc(r, "done")
       ELSE /\ SetPc(r, IF WaitChecksDisconnect THEN "checkDone" ELSE "checkCtx")
            /\ UNCHANGED <<lock, result>>
    /\ CallerVars(r)
    /\ UNCHANGED <<sentReq, pending, waitSet, ch, resp, waker>>

\* select { case <-c.Done(): return peerDisconnectedErr; default: }
BarrierChecksDone(r) ==
    /\ callerPc[r] = "checkDone"
    /\ IF connDone
       THEN /\ lock' = None /\ SetResult(r, "disconnected") /\ SetPc(r, "done")
       ELSE /\ SetPc(r, "checkCtx") /\ UNCHANGED <<lock, result>>
    /\ CallerVars(r)
    /\ UNCHANGED <<sentReq, pending, waitSet, ch, resp, waker>>

\* select { case <-ctx.Done(): ...; default: } then c.notifyCond.Wait(),
\* which releases notifyMu and sleeps in one step.
BarrierChecksCtxOrWaits(r) ==
    /\ callerPc[r] = "checkCtx"
    /\ lock' = None
    /\ IF CtxDone(r)
       THEN /\ SetResult(r, IF connDone THEN "disconnected" ELSE "cancelled")
            /\ SetPc(r, "done") /\ UNCHANGED waitSet
       ELSE /\ waitSet' = waitSet \cup {r} /\ SetPc(r, "waiting")
            /\ UNCHANGED result
    /\ CallerVars(r)
    /\ UNCHANGED <<sentReq, pending, ch, resp, waker>>

\* Wait() returns after a Broadcast: take notifyMu again.
BarrierRelocks(r) ==
    /\ callerPc[r] = "relock" /\ lock = None
    /\ lock' = r
    /\ SetPc(r, "loop")
    /\ CallerVars(r)
    /\ UNCHANGED <<sentReq, pending, waitSet, ch, resp, waker, result>>

\* The app cancels the caller's ctx. No fairness.
AppCancelsCtx(r) ==
    /\ CtxCanCancel /\ r \in AppCallers /\ ~ctxCanceled[r]
    /\ callerPc[r] \notin {"idle", "done"}
    /\ ctxCanceled' = [ctxCanceled EXCEPT ![r] = TRUE]
    /\ UNCHANGED <<peerVars, sentReq, recvPc, recvTmp, lastEnq, completed, queue, qClosed,
                   final, connDone, inboundDone, lock, waitSet, workerPc, cur, callerPc,
                   pending, ch, resp, waker, connWatcher, drainWaker, drainPc,
                   timerFired, historyVars>>
    /\ UNCHANGED inVars

-----------------------------------------------------------------------------
(* Goroutines that wake cond waiters. *)

BroadcasterVars ==
    UNCHANGED <<peerVars, sentReq, recvPc, recvTmp, lastEnq, completed, queue, qClosed,
                final, connDone, inboundDone, lock, workerPc, cur, pending, ch, resp,
                ctxCanceled, timerFired, historyVars, inVars>>

\* waitNotificationsUpTo's goroutine: case <-ctx.Done(), then Broadcast
\* under notifyMu.
CtxWakerFires(r) ==
    /\ waker[r] = "armed" /\ CtxDone(r) /\ lock = None
    /\ Broadcast
    /\ waker' = [waker EXCEPT ![r] = "off"]
    /\ UNCHANGED <<connWatcher, drainWaker>>
    /\ BroadcasterVars

\* The same goroutine: case <-stopWake after waitNotificationsUpTo returns.
CtxWakerStops(r) ==
    /\ waker[r] = "armed" /\ callerPc[r] = "done"
    /\ waker' = [waker EXCEPT ![r] = "off"]
    /\ UNCHANGED <<callerPc, drainPc, waitSet, connWatcher, drainWaker>>
    /\ BroadcasterVars

\* NewConnection's goroutine: <-c.ctx.Done(), then Broadcast.
ConnWatcherFires ==
    /\ connWatcher = "armed" /\ connDone /\ lock = None
    /\ Broadcast
    /\ connWatcher' = "fired"
    /\ UNCHANGED <<waker, drainWaker>>
    /\ BroadcasterVars

\* waitForNotificationDrain's goroutine: the timeout ctx ends, then Broadcast.
DrainWakerFires ==
    /\ drainWaker = "armed" /\ timerFired /\ lock = None
    /\ Broadcast
    /\ drainWaker' = "off"
    /\ UNCHANGED <<waker, connWatcher>>
    /\ BroadcasterVars

DrainWakerStops ==
    /\ drainWaker = "armed" /\ drainPc \in {"cancel", "done"}
    /\ drainWaker' = "off"
    /\ UNCHANGED <<callerPc, drainPc, waitSet, waker, connWatcher>>
    /\ BroadcasterVars

-----------------------------------------------------------------------------
(* The goroutine that shutdownReceive starts:                              *)
(* waitForNotificationDrain(finalEnqueuedSeq, 5s), then c.inboundCancel.   *)

DrainVars ==
    UNCHANGED <<peerVars, sentReq, recvPc, recvTmp, lastEnq, completed, queue, qClosed,
                final, connDone, workerPc, cur, callerPc, pending, ch, resp,
                ctxCanceled, waker, connWatcher, timerFired, historyVars, inVars>>

DrainStarts ==
    /\ drainPc = "start"
    /\ IF ~DrainBeforeInboundCancel \/ final = 0
       THEN /\ drainPc' = "cancel" /\ UNCHANGED <<lock, drainWaker>>
       ELSE /\ lock = None /\ lock' = Drainer
            /\ drainWaker' = IF DrainTimerWaker THEN "armed" ELSE "off"
            /\ drainPc' = "loop"
    /\ UNCHANGED <<inboundDone, waitSet>>
    /\ DrainVars

DrainLoop ==
    /\ drainPc = "loop"
    /\ IF completed >= final
       THEN /\ lock' = None /\ drainPc' = "cancel"
       ELSE /\ drainPc' = "checkTimeout" /\ UNCHANGED lock
    /\ UNCHANGED <<inboundDone, waitSet, drainWaker>>
    /\ DrainVars

\* if ctx.Err() != nil { return } then c.notifyCond.Wait()
DrainChecksTimeoutOrWaits ==
    /\ drainPc = "checkTimeout"
    /\ lock' = None
    /\ IF timerFired /\ DrainChecksTimeout
       THEN /\ drainPc' = "cancel" /\ UNCHANGED waitSet
       ELSE /\ waitSet' = waitSet \cup {Drainer} /\ drainPc' = "waiting"
    /\ UNCHANGED <<inboundDone, drainWaker>>
    /\ DrainVars

DrainRelocks ==
    /\ drainPc = "relock" /\ lock = None
    /\ lock' = Drainer /\ drainPc' = "loop"
    /\ UNCHANGED <<inboundDone, waitSet, drainWaker>>
    /\ DrainVars

\* c.inboundCancel(cause): handler contexts end.
InboundCancel ==
    /\ drainPc = "cancel"
    /\ inboundDone' = TRUE /\ drainPc' = "done"
    /\ UNCHANGED <<lock, waitSet, drainWaker>>
    /\ DrainVars

\* notificationQueueDrainTimeout elapses.
DrainTimerFires ==
    /\ DrainTimeoutFires /\ ~timerFired
    /\ drainPc \in {"loop", "checkTimeout", "waiting", "relock"}
    /\ timerFired' = TRUE
    /\ UNCHANGED <<peerVars, sentReq, recvPc, recvTmp, lastEnq, completed, queue, qClosed,
                   final, connDone, inboundDone, lock, waitSet, workerPc, cur, callerPc,
                   pending, ch, resp, ctxCanceled, waker, connWatcher, drainWaker,
                   drainPc, historyVars>>
    /\ UNCHANGED inVars

-----------------------------------------------------------------------------
(* Requests from the peer. receive() snapshots the watermark and starts one *)
(* goroutine per request. The goroutine waits with waitNotificationsUpTo, *)
(* modeled by the caller barrier actions above with q as the caller.      *)

\* The peer writes a request. No fairness.
PeerSendsInReq(q) ==
    /\ ~eofSent /\ q \notin inSent
    /\ inSent' = inSent \cup {q}
    /\ wire' = Append(wire, Msg("req", 0, "none", q))
    /\ UNCHANGED <<notesSent, nestedSent, respSent, eofSent, sentReq, recvPc, recvTmp,
                   lastEnq, completed, queue, qClosed, final, connDone, inboundDone, lock,
                   waitSet, workerPc, cur, callerPc, pending, ch, resp, ctxCanceled,
                   waker, connWatcher, drainWaker, drainPc, timerFired, historyVars,
                   cancelSent, inflight, inPc, inStart>>

\* The peer writes $/cancel_request for a request that it sent. No fairness.
PeerCancelsInReq(q) ==
    /\ PeerCanCancelReq /\ ~eofSent /\ q \in inSent \ cancelSent
    /\ cancelSent' = cancelSent \cup {q}
    /\ wire' = Append(wire, Msg("cancel", 0, "none", q))
    /\ UNCHANGED <<notesSent, nestedSent, respSent, eofSent, sentReq, recvPc, recvTmp,
                   lastEnq, completed, queue, qClosed, final, connDone, inboundDone, lock,
                   waitSet, workerPc, cur, callerPc, pending, ch, resp, ctxCanceled,
                   waker, connWatcher, drainWaker, drainPc, timerFired, historyVars,
                   inSent, inflight, inPc, inStart>>

\* receive: watermark := c.lastEnqueuedNotificationSeq under notifyMu, then
\* c.inflight[idKey] = cancel under c.mu, then go func(...). Only receive()
\* writes lastEnqueuedNotificationSeq and reads $/cancel_request, so one step
\* loses no interleaving. The goroutine starts at the barrier.
RecvRequest ==
    /\ recvPc = "read" /\ wire # <<>> /\ Head(wire).t = "req" /\ lock = None
    /\ LET q == Head(wire).r IN
       /\ resp' = [resp EXCEPT ![q] = IF InboundWaits THEN lastEnq ELSE 0]
       /\ readBefore' = [readBefore EXCEPT ![q] = Range(received)]
       /\ callerPc' = [callerPc EXCEPT ![q] = "barrier"]
       /\ inflight' = inflight \cup {q}
       /\ inPc' = [inPc EXCEPT ![q] = "waitBarrier"]
    /\ wire' = Tail(wire)
    /\ UNCHANGED <<notesSent, nestedSent, respSent, eofSent, sentReq, recvPc, recvTmp,
                   lastEnq, completed, queue, qClosed, final, connDone, inboundDone, lock,
                   waitSet, workerPc, cur, pending, ch, ctxCanceled, waker, connWatcher,
                   drainWaker, drainPc, timerFired, received, handled, respRead, result,
                   finalIds, nestedFrom, inSent, cancelSent, inStart>>

\* receive -> handleCancelRequest: cancel := c.inflight[idKey] under c.mu,
\* then cancel(context.Canceled). A request that already left c.inflight is
\* ignored.
RecvCancel ==
    /\ recvPc = "read" /\ wire # <<>> /\ Head(wire).t = "cancel"
    /\ LET q == Head(wire).r IN
       ctxCanceled' = IF q \in inflight THEN [ctxCanceled EXCEPT ![q] = TRUE] ELSE ctxCanceled
    /\ wire' = Tail(wire)
    /\ UNCHANGED <<notesSent, nestedSent, respSent, eofSent, sentReq, recvPc, recvTmp,
                   lastEnq, completed, queue, qClosed, final, connDone, inboundDone, lock,
                   waitSet, workerPc, cur, callerPc, pending, ch, resp, waker,
                   connWatcher, drainWaker, drainPc, timerFired, historyVars, inVars>>

InVarsBut(q) ==
    UNCHANGED <<peerVars, sentReq, recvPc, recvTmp, lastEnq, completed, queue, qClosed,
                final, connDone, inboundDone, lock, waitSet, workerPc, cur, callerPc,
                pending, ch, resp, ctxCanceled, waker, connWatcher, drainWaker, drainPc,
                timerFired, historyVars, inSent, cancelSent>>

\* if err := c.waitNotificationsUpTo(reqCtx, watermark); err != nil { ... }
\* A nil error starts the handler.
InAfterBarrier(q) ==
    /\ inPc[q] = "waitBarrier" /\ callerPc[q] = "done"
    /\ IF result[q] = "ok"
       THEN /\ inPc' = [inPc EXCEPT ![q] = "handler"]
            /\ inStart' = [inStart EXCEPT ![q] = "live"]
       ELSE /\ inPc' = [inPc EXCEPT ![q] = "checkDone"]
            /\ UNCHANGED inStart
    /\ UNCHANGED inflight
    /\ InVarsBut(q)

\* select { case <-c.Done(): c.handleInbound(reqCtx, m); default: }, else
\* c.sendInboundRequestError(m, toReqErr(err)). The deferred delete from
\* c.inflight follows the reply; the model folds it into the reply step.
InChecksDone(q) ==
    /\ inPc[q] = "checkDone"
    /\ IF connDone
       THEN /\ inPc' = [inPc EXCEPT ![q] = "handler"]
            /\ inStart' = [inStart EXCEPT ![q] = "disconnected"]
            /\ UNCHANGED inflight
       ELSE /\ inPc' = [inPc EXCEPT ![q] = "errReplied"]
            /\ inflight' = inflight \ {q}
            /\ UNCHANGED inStart
    /\ InVarsBut(q)

\* The request handler returns promptly and handleInbound sends the reply.
InHandlerReturns(q) ==
    /\ inPc[q] = "handler"
    /\ inPc' = [inPc EXCEPT ![q] = "replied"]
    /\ inflight' = inflight \ {q}
    /\ UNCHANGED inStart
    /\ InVarsBut(q)

-----------------------------------------------------------------------------

\* Every SDK goroutine is at rest. Stutter here so that TLC reports only real
\* deadlocks.
AtRest ==
    /\ wire = <<>> \/ recvPc = "exited"
    /\ recvPc \in {"read", "exited"}
    /\ queue = <<>> /\ workerPc \in {"dequeue", "exited"}
    /\ \A r \in Reqs : callerPc[r] \in {"idle", "done"}
    /\ drainPc \in {"off", "done"}
    /\ \A q \in InReqs : inPc[q] \in {"none", "replied", "errReplied"}

Rest == AtRest /\ UNCHANGED vars

SDKStep ==
    \/ RecvNotification \/ RecvResponse \/ RecvDeliver \/ RecvEOF
    \/ ShutdownCancel \/ ShutdownClose
    \/ WorkerDequeue \/ WorkerExit \/ PlainHandlerReturns \/ NestedHandlerCalls
    \/ NestedHandlerReturns \/ WorkerComplete
    \/ ConnWatcherFires \/ DrainWakerFires \/ DrainWakerStops
    \/ DrainStarts \/ DrainLoop \/ DrainChecksTimeoutOrWaits \/ DrainRelocks
    \/ InboundCancel
    \/ RecvRequest \/ RecvCancel
    \/ \E q \in InReqs : InAfterBarrier(q) \/ InChecksDone(q) \/ InHandlerReturns(q)
    \/ \E r \in Reqs :
          \/ SendsRequest(r) \/ GetsResponse(r) \/ SeesCtxDone(r)
          \/ CtxBranchChecksDone(r) \/ SeesDisconnect(r) \/ CleansUp(r)
          \/ EntersBarrier(r) \/ BarrierLoop(r) \/ BarrierChecksDone(r)
          \/ BarrierChecksCtxOrWaits(r) \/ BarrierRelocks(r)
          \/ CtxWakerFires(r) \/ CtxWakerStops(r)

EnvStep ==
    \/ \E k \in NoteKinds : PeerSendsNote(k)
    \/ \E r \in Reqs : PeerResponds(r)
    \/ PeerCloses
    \/ \E q \in InReqs : PeerSendsInReq(q) \/ PeerCancelsInReq(q)
    \/ \E r \in AppCallers : AppCalls(r) \/ AppCancelsCtx(r)
    \/ SlowHandlerReturns
    \/ DrainTimerFires

Next == SDKStep \/ EnvStep \/ Rest

\* Each Go goroutine runs when it can. The peer, the app, slow handlers and
\* the drain timer have no fairness here; the timer gets it in drain-timeout.cfg.
Fairness ==
    /\ WF_vars(RecvNotification) /\ WF_vars(RecvResponse) /\ WF_vars(RecvDeliver)
    /\ WF_vars(RecvEOF) /\ WF_vars(ShutdownCancel) /\ WF_vars(ShutdownClose)
    /\ WF_vars(WorkerDequeue) /\ WF_vars(WorkerExit) /\ WF_vars(PlainHandlerReturns)
    /\ WF_vars(NestedHandlerCalls) /\ WF_vars(NestedHandlerReturns)
    /\ WF_vars(WorkerComplete)
    /\ WF_vars(ConnWatcherFires) /\ WF_vars(DrainWakerFires) /\ WF_vars(DrainWakerStops)
    /\ WF_vars(DrainStarts) /\ WF_vars(DrainLoop) /\ WF_vars(DrainChecksTimeoutOrWaits)
    /\ WF_vars(DrainRelocks) /\ WF_vars(InboundCancel)
    /\ WF_vars(RecvRequest) /\ WF_vars(RecvCancel)
    /\ \A q \in InReqs :
          WF_vars(InAfterBarrier(q)) /\ WF_vars(InChecksDone(q)) /\ WF_vars(InHandlerReturns(q))
    /\ \A r \in Reqs :
          /\ WF_vars(SendsRequest(r)) /\ WF_vars(GetsResponse(r))
          /\ WF_vars(SeesCtxDone(r)) /\ WF_vars(CtxBranchChecksDone(r))
          /\ WF_vars(SeesDisconnect(r)) /\ WF_vars(CleansUp(r))
          /\ WF_vars(EntersBarrier(r)) /\ WF_vars(BarrierLoop(r))
          /\ WF_vars(BarrierChecksDone(r)) /\ WF_vars(BarrierChecksCtxOrWaits(r))
          /\ WF_vars(BarrierRelocks(r))
          /\ WF_vars(CtxWakerFires(r)) /\ WF_vars(CtxWakerStops(r))

Spec == Init /\ [][Next]_vars /\ Fairness

\* drain-timeout.cfg: the 5 s timer always fires in the end.
TimerSpec == Spec /\ WF_vars(DrainTimerFires)

\* nested-handler.cfg: the peer answers the handler's request in the end, or
\* closes. Without this, a request that waits for that handler waits on the
\* peer, as it waits on a slow handler.
NestedSpec == Spec /\ WF_vars(PeerResponds(Nested))

-----------------------------------------------------------------------------
(* Properties. Each says one thing that a user of the SDK relies on. *)

\* The notifications whose handlers a request waits for: those read before
\* its response. A request made by a handler with its own ctx cannot wait for
\* that handler, so it waits only for the notifications read before the
\* handler's own (the SendRequest doc comment). Ids follow the read order.
Promised(r) ==
    IF r = Nested THEN {i \in readBefore[r] : i < nestedFrom} ELSE readBefore[r]

\* When SendRequest returns a response, the handler of every notification
\* that it promises has returned. (Prompt() returns after every
\* session/update of the turn is handled.)
BarrierHolds ==
    \A r \in Reqs \ InReqs : result[r] = "ok" => Promised(r) \subseteq HandledSet

\* After a disconnect, handlers of notifications that were already read keep
\* a live ctx until they return, or until the 5 s drain timeout.
InboundCtxOutlivesDrain ==
    inboundDone => (finalIds \subseteq HandledSet \/ timerFired)

Started(r)  == callerPc[r] # "idle"
Returned(r) == callerPc[r] = "done"

\* Once the response is read, the caller returns without a disconnect, a
\* ctx cancel or the drain timeout.
ResponseReturnsCaller == \A r \in Reqs : [](respRead[r] => <>Returned(r))

\* A canceled ctx makes the caller return, also while it waits for a
\* handler that does not finish.
CancelReturnsCaller == \A r \in Reqs : [](Started(r) /\ CtxDone(r) => <>Returned(r))

\* A disconnect makes the caller return, also while it waits for a handler
\* that does not finish, and without the drain timeout.
DisconnectReturnsCaller == \A r \in Reqs : [](Started(r) /\ connDone => <>Returned(r))

\* After a disconnect, handler contexts end in the end.
ShutdownCompletes == [](connDone => <>inboundDone)

\* An inbound request handler that starts while the connection is up starts
\* only after the handler of every notification read before the request
\* has returned. (The permission prompt prints after its tool call.)
InboundHandlerAfterNotes ==
    \A q \in InReqs : inStart[q] = "live" => readBefore[q] \subseteq HandledSet

\* The same without the disconnect exception. It fails by design: after a
\* disconnect the handler starts with a canceled ctx, while an earlier
\* handler can still run.
InboundHandlerAfterNotesEvenOnDisconnect ==
    \A q \in InReqs : inStart[q] # "none" => readBefore[q] \subseteq HandledSet

\* waitNotificationsUpTo panics when its target exceeds
\* lastEnqueuedNotificationSeq, or when completed exceeds it.
NoWatermarkPanic ==
    /\ completed <= lastEnq
    /\ \A r \in Reqs : callerPc[r] \in {"loop", "checkDone", "checkCtx", "waiting", "relock"}
                         => resp[r] <= lastEnq

\* Once read, an inbound request's handler starts, unless the peer canceled
\* it first, when earlier handlers return.
InboundHandlerStarts ==
    \A q \in InReqs : [](inPc[q] # "none" => <>(inStart[q] # "none" \/ ctxCanceled[q]))

\* Every inbound request gets one reply: the handler's, or the cancel error.
InboundRequestAnswered ==
    \A q \in InReqs : [](inPc[q] # "none" => <>(inPc[q] \in {"replied", "errReplied"}))

=============================================================================
