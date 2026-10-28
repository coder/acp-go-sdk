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
    \* Facts read from connection.go by check.sh:
    WaitsForBarrier,          \* SendRequest and SendRequestNoResult call waitNotificationsUpTo
    WatermarkSource,          \* "enqueued" or "completed": the counter handleResponse records
    CompletesAfterHandler,    \* processNotifications counts a notification after handleInbound returns
    WorkerBroadcasts,         \* processNotifications calls notifyCond.Broadcast after counting
    CtxWakerSpawned,          \* waitNotificationsUpTo starts the ctx.Done broadcaster
    ConnDoneWaker,            \* NewConnection starts the c.ctx.Done broadcaster
    DrainBeforeInboundCancel, \* shutdownReceive drains before inboundCancel
    DrainTimerWaker,          \* waitForNotificationDrain starts the timeout broadcaster
    RollsBackOnOverflow       \* receive decrements lastEnqueuedNotificationSeq on overflow

ASSUME WatermarkSource \in {"enqueued", "completed"}
ASSUME NoteKinds \subseteq {"plain", "slow", "nested"}

Nested  == "nested"   \* the request made from inside a "nested" handler
Drainer == "drainer"  \* the drain goroutine as a cond waiter
None    == "none"
Empty   == -1         \* an empty pendingResponse.ch

Reqs == AppCallers \cup (IF "nested" \in NoteKinds THEN {Nested} ELSE {})

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
    received, handled, readBefore, respRead, result, finalIds

vars == <<wire, notesSent, nestedSent, respSent, eofSent, sentReq,
          recvPc, recvTmp, lastEnq, completed, queue, qClosed, final,
          connDone, inboundDone, lock, waitSet, workerPc, cur,
          callerPc, pending, ch, resp, ctxCanceled, waker,
          connWatcher, drainWaker, drainPc, timerFired,
          received, handled, readBefore, respRead, result, finalIds>>

peerVars   == <<wire, notesSent, nestedSent, respSent, eofSent>>
historyVars == <<received, handled, readBefore, respRead, result, finalIds>>

Range(s) == {s[i] : i \in DOMAIN s}
HandledSet == Range(handled)

\* The ctx that a caller passes. A handler gets c.inboundCtx.
CtxDone(r) == IF r = Nested THEN inboundDone ELSE ctxCanceled[r]

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
    /\ result = [r \in Reqs |-> "none"] /\ finalIds = {}

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

PeerResponds(r) ==
    /\ ~eofSent /\ r \in sentReq \ respSent
    /\ respSent' = respSent \cup {r}
    /\ wire' = Append(wire, Msg("resp", 0, "none", r))
    /\ UNCHANGED <<notesSent, nestedSent, eofSent, sentReq, recvPc, recvTmp, lastEnq,
                   completed, queue, qClosed, final, connDone, inboundDone, lock,
                   waitSet, workerPc, cur, callerPc, pending, ch, resp, ctxCanceled,
                   waker, connWatcher, drainWaker, drainPc, timerFired, historyVars>>

PeerCloses ==
    /\ PeerCanClose /\ ~eofSent
    /\ eofSent' = TRUE
    /\ wire' = Append(wire, Msg("eof", 0, "none", None))
    /\ UNCHANGED <<notesSent, nestedSent, respSent, sentReq, recvPc, recvTmp, lastEnq,
                   completed, queue, qClosed, final, connDone, inboundDone, lock,
                   waitSet, workerPc, cur, callerPc, pending, ch, resp, ctxCanceled,
                   waker, connWatcher, drainWaker, drainPc, timerFired, historyVars>>

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
                   result, finalIds>>

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
                   drainPc, timerFired, received, handled, result, finalIds>>

\* handleResponse: pr.ch <- responseEnvelope{...}. The channel has room for one.
RecvDeliver ==
    /\ recvPc = "deliver"
    /\ ch' = [ch EXCEPT ![recvTmp.r] = recvTmp.w]
    /\ recvPc' = "read"
    /\ UNCHANGED <<peerVars, sentReq, recvTmp, lastEnq, completed, queue, qClosed, final,
                   connDone, inboundDone, lock, waitSet, workerPc, cur, callerPc,
                   pending, resp, ctxCanceled, waker, connWatcher, drainWaker, drainPc,
                   timerFired, historyVars>>

\* receive: scanner.Scan() returns false, then shutdownReceive(cause).
RecvEOF ==
    /\ recvPc = "read" /\ wire # <<>> /\ Head(wire).t = "eof"
    /\ wire' = Tail(wire)
    /\ recvPc' = "shutCancel"
    /\ UNCHANGED <<notesSent, nestedSent, respSent, eofSent, sentReq, recvTmp, lastEnq,
                   completed, queue, qClosed, final, connDone, inboundDone, lock,
                   waitSet, workerPc, cur, callerPc, pending, ch, resp, ctxCanceled,
                   waker, connWatcher, drainWaker, drainPc, timerFired, historyVars>>

\* shutdownReceive: c.cancel(cause), so c.Done() closes.
ShutdownCancel ==
    /\ recvPc = "shutCancel"
    /\ connDone' = TRUE
    /\ recvPc' = "shutClose"
    /\ UNCHANGED <<peerVars, sentReq, recvTmp, lastEnq, completed, queue, qClosed, final,
                   inboundDone, lock, waitSet, workerPc, cur, callerPc, pending, ch,
                   resp, ctxCanceled, waker, connWatcher, drainWaker, drainPc,
                   timerFired, historyVars>>

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
                   received, handled, readBefore, respRead, result>>

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

\* The range loop ends: the queue is closed and empty.
WorkerExit ==
    /\ workerPc = "dequeue" /\ queue = <<>> /\ qClosed
    /\ workerPc' = "exited"
    /\ UNCHANGED <<peerVars, sentReq, recvPc, recvTmp, lastEnq, completed, queue, qClosed,
                   final, connDone, inboundDone, lock, waitSet, cur, callerPc, pending,
                   ch, resp, ctxCanceled, waker, connWatcher, drainWaker, drainPc,
                   timerFired, historyVars>>

AfterHandler == IF CompletesAfterHandler THEN "complete" ELSE "dequeue"

HandlerReturnsVars ==
    UNCHANGED <<peerVars, sentReq, recvPc, recvTmp, lastEnq, completed, queue, qClosed,
                final, connDone, inboundDone, lock, waitSet, cur, callerPc, pending, ch,
                resp, ctxCanceled, waker, connWatcher, drainWaker, drainPc, timerFired,
                received, readBefore, respRead, result, finalIds>>

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

\* A handler that calls SendRequest on the same connection with its ctx
\* (c.inboundCtx), for example a SessionUpdate handler that calls
\* CallExtension. The call runs on this goroutine.
NestedHandlerCalls ==
    /\ workerPc = "handle" /\ cur.kind = Nested /\ callerPc[Nested] = "idle"
    /\ callerPc' = [callerPc EXCEPT ![Nested] = "registered"]
    /\ pending' = pending \cup {Nested}
    /\ workerPc' = "nestedCall"
    /\ UNCHANGED <<peerVars, sentReq, recvPc, recvTmp, lastEnq, completed, queue, qClosed,
                   final, connDone, inboundDone, lock, waitSet, cur, ch, resp,
                   ctxCanceled, waker, connWatcher, drainWaker, drainPc, timerFired,
                   historyVars>>

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

-----------------------------------------------------------------------------
(* SendRequest / SendRequestNoResult, one per caller r. *)

CallerVars(r) ==
    UNCHANGED <<peerVars, recvPc, recvTmp, lastEnq, completed, queue, qClosed, final,
                connDone, inboundDone, workerPc, cur, ctxCanceled, connWatcher,
                drainWaker, drainPc, timerFired, received, handled, readBefore,
                respRead, finalIds>>

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

\* waitNotificationsUpTo(ctx, watermark): target 0 returns at once.
\* Otherwise take notifyMu and start the ctx.Done broadcaster.
EntersBarrier(r) ==
    /\ callerPc[r] = "barrier"
    /\ IF ~WaitsForBarrier \/ resp[r] = 0
       THEN /\ SetResult(r, "ok") /\ SetPc(r, "done")
            /\ UNCHANGED <<lock, waker>>
       ELSE /\ lock = None /\ lock' = r
            /\ waker' = [waker EXCEPT ![r] = IF CtxWakerSpawned THEN "armed" ELSE "off"]
            /\ SetPc(r, "loop") /\ UNCHANGED result
    /\ CallerVars(r)
    /\ UNCHANGED <<sentReq, pending, waitSet, ch, resp>>

\* for c.completedNotificationSeq < target { ... } (holds notifyMu)
BarrierLoop(r) ==
    /\ callerPc[r] = "loop"
    /\ IF completed >= resp[r]
       THEN /\ lock' = None /\ SetResult(r, "ok") /\ SetPc(r, "done")
       ELSE /\ SetPc(r, "checkDone") /\ UNCHANGED <<lock, result>>
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

-----------------------------------------------------------------------------
(* Goroutines that wake cond waiters. *)

BroadcasterVars ==
    UNCHANGED <<peerVars, sentReq, recvPc, recvTmp, lastEnq, completed, queue, qClosed,
                final, connDone, inboundDone, lock, workerPc, cur, pending, ch, resp,
                ctxCanceled, timerFired, historyVars>>

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
                ctxCanceled, waker, connWatcher, timerFired, historyVars>>

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
    /\ IF timerFired
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

-----------------------------------------------------------------------------

\* Every SDK goroutine is at rest. Stutter here so that TLC reports only real
\* deadlocks.
AtRest ==
    /\ wire = <<>> \/ recvPc = "exited"
    /\ recvPc \in {"read", "exited"}
    /\ queue = <<>> /\ workerPc \in {"dequeue", "exited"}
    /\ \A r \in Reqs : callerPc[r] \in {"idle", "done"}
    /\ drainPc \in {"off", "done"}

Rest == AtRest /\ UNCHANGED vars

SDKStep ==
    \/ RecvNotification \/ RecvResponse \/ RecvDeliver \/ RecvEOF
    \/ ShutdownCancel \/ ShutdownClose
    \/ WorkerDequeue \/ WorkerExit \/ PlainHandlerReturns \/ NestedHandlerCalls
    \/ NestedHandlerReturns \/ WorkerComplete
    \/ ConnWatcherFires \/ DrainWakerFires \/ DrainWakerStops
    \/ DrainStarts \/ DrainLoop \/ DrainChecksTimeoutOrWaits \/ DrainRelocks
    \/ InboundCancel
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

-----------------------------------------------------------------------------
(* Properties. Each says one thing that a user of the SDK relies on. *)

\* When SendRequest returns a response, the handler of every notification
\* that the peer sent before that response has returned. (Prompt() returns
\* after every session/update of the turn is handled.)
BarrierHolds ==
    \A r \in Reqs : result[r] = "ok" => readBefore[r] \subseteq HandledSet

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

=============================================================================
