---
name: verify-notification-barrier
description: Model-check the notification barrier in connection.go with TLA+ (TLC). The barrier makes SendRequest (and so Prompt and every other request) return only after the handlers of all notifications that arrived before its response have finished, and makes shutdown drain those handlers. It also makes the handler of a request from the peer start only after the handlers of notifications read before that request. Use before you finish any change to NewConnection, receive, handleCancelRequest, shutdownReceive, processNotifications, handleResponse, SendRequest, SendRequestNoResult, waitForResponse, waitNotificationsUpTo, waitNotifications, waitInboundBarrier, startPeerWait or waitForNotificationDrain, and when a bug report says that Prompt returned before its session/update handlers finished, that a request hangs, or that a handler context ended too early.
---

# Verify the notification barrier

`connection.go` has one reader goroutine, one goroutine that runs notification handlers in order, any number of goroutines that wait in `SendRequest`, and one goroutine for each request from the peer. Four fixes already went into this code (#5, #8, #10, #30), and unit tests reach few of its interleavings. For example, `go test ./...` still passes when `SendRequestNoResult` stops waiting for earlier handlers, when the wait loop stops checking for a disconnect, or when the drain loop stops checking its timeout. `check.sh` fails on all three. This skill checks every interleaving of a small model of that code with TLC.

The model is a hand translation. `check.sh` reads eighteen facts from `connection.go` on every run and sets the model from them, so a change to one of those lines changes what TLC checks. Any other change inside a covered function makes `check.sh` report STALE until you review the model against the code.

## What it proves

Each property is one promise to SDK users. The config names show where TLC checks it.

| Property | Promise | Harm if broken | Configs |
| --- | --- | --- | --- |
| `BarrierHolds` | When `SendRequest` returns a response, the handler of every notification that the peer sent before that response has returned. A request that a handler makes with its own ctx waits only for the notifications before that handler's own, as the `SendRequest` doc comment says. | `Prompt()` returns while `session/update` handlers still run, so a client renders the end of a turn before its last chunks. | all |
| `InboundCtxOutlivesDrain` | After a disconnect, handlers of notifications that were already read keep a live ctx until they return, or until the 5 s drain timeout. | Handlers that respect ctx drop the last messages of a session. | all |
| `ResponseReturnsCaller` | Once the response is read, the caller returns. It needs no disconnect, no ctx cancel and no timeout. This includes a request that a notification handler makes on the same connection with its own ctx. The configs that check it have only handlers that return in the end, so nested-handler.cfg assumes that the peer answers the handler's request in the end. | A request hangs although its answer arrived. When a handler's request hangs, the client also handles no later notification until the peer exits (`TestSendRequest_FromNotificationHandlerReturns`). | barrier, deep, nested-handler, inbound-nested |
| `CancelReturnsCaller` | A canceled ctx makes the caller return, also while a handler that it waits for never finishes. For a request from the peer, `$/cancel_request` is the cancel. | Cancel does not stop a stuck request. | slow-handler, inbound-slow-handler |
| `DisconnectReturnsCaller` | A disconnect makes the caller return at once, also while a handler that it waits for never finishes. | Callers hang after the peer exits. | slow-handler, inbound-slow-handler |
| `InboundHandlerAfterNotes` | When a request from the peer leaves its wait with no error while the connection is up, the handler of every notification read before that request has returned, or the handler that runs at that moment waits on the peer's response to a request that it made with its ctx. The peer can need our answer before it sends that response. The promise is about the end of the wait. The handler starts later, and by then that peer wait can have ended. | The client shows the permission prompt before the tool call that it asks about. | inbound-* |
| `CancelBeforeCheckSkipsHandler` | A request from the peer whose ctx has ended when `waitInboundBarrier` checks it does not start its handler as usual. After a `$/cancel_request`, it gets the cancel error. Only a disconnect starts the handler, with the ended ctx. A cancel that lands after the check can still meet a running handler. | A cancelled request still runs its handler, and the peer gets a result for a request that it cancelled. | inbound-request |
| `NoWatermarkPanic` | No wait target exceeds `lastEnqueuedNotificationSeq`, so the panics in `waitNotifications` cannot fire. | The SDK panics. | inbound-* |
| `InboundHandlerStarts` | The handler of a request from the peer starts when the earlier handlers return, unless the peer canceled the request first. | A request from the peer hangs. | inbound-request |
| `InboundRequestAnswered` | Every request from the peer gets one reply: the handler's result or the cancel error. This includes a request that the peer must get an answer to before it answers the running handler's request. | The peer waits forever. When the running handler waits on the peer, the handler, the peer's request and every later notification hang. | inbound-request, inbound-nested, inbound-nested-peer |
| `ShutdownCompletes` | After a disconnect, handler contexts end in the end. | Goroutines and handler work leak after every session. | barrier, drain-timeout, deep |
| `NoDeadlock` (`CHECK_DEADLOCK`) | No state exists where every goroutine is stuck and nothing can move. | The connection freezes. | all |

After a disconnect, the handler of a request from the peer starts with a canceled ctx, also while an earlier notification handler still runs. The code comment in `receive` says so, and no property promises the order then.

A slow notification handler delays every later request from the peer until it returns, the peer cancels the request, or the peer disconnects. That is the price of the order. `inbound-slow-handler.cfg` does not check `InboundHandlerStarts`, because the slow handler makes it fail.

A request from the peer leaves its wait early while the running handler waits on the peer, from `startPeerWait` to `donePeerWait`. `inbound-nested-peer.cfg` checks the cycle that this breaks:

1. The peer sends a `session/update`. `processNotifications` calls its handler with `handlerCtx`, and the handler calls a request on the same connection with that ctx, such as `CallExtension`. `startPeerWait` counts it in `c.peerWaits[1]`.
2. The peer sends its own request, and answers step 1 only after our reply. `receive` reads watermark 1 and starts the request goroutine.
3. `waitNotifications(ctx, 1, true)` returns, because `c.peerWaits[completedNotificationSeq+1]` is 1. The handler runs and replies, and the peer answers step 1.

Without step 3, the handler and the peer's request wait for each other until the peer closes the stream. The early start also skips the notifications between the running one and the watermark, and the wait can end after the response arrived but before `donePeerWait` runs. The handler starts in a later step, so it can start after the peer wait ended, while the earlier handler still runs. The rule in the code allows all three.

## When to run it

Run it when `git diff` touches any covered function (see the coverage record at the end), or when the code that you changed writes `lastEnqueuedNotificationSeq`, `completedNotificationSeq`, `notificationQueue`, `pending`, `notifyCond`, `inboundCtx`, `c.ctx`, `c.inflight`, `c.peerWaits` or the handler ctx value `handlingNotification`. Run it once before your change, so that you know the starting result, and once after it.

## Run

From the repository root:

```bash
.agents/skills/verify-notification-barrier/check.sh --out "$OUT_DIR"
```

- It needs Java 11 or newer, `curl`, `sha256sum` and `awk`.
- The first run downloads `tla2tools.jar` 1.7.4 into `${ACP_TLA_CACHE:-${XDG_CACHE_HOME:-~/.cache}/acp-go-sdk-tla}` and checks its sha256 on every run. To use a copy that you already have, set `TLA2TOOLS_JAR`. To pick a JVM, set `JAVA`. The default heap is 4 GB: set `TLC_HEAP` to change it.
- It took 7 to 11 minutes on a busy shared host, most of it in `inbound-nested.cfg` and `nested-handler.cfg`. `--deep` adds `deep-two-callers.cfg`: two app goroutines call at once, which takes about 15 more minutes (1,127,364 states at the sync commit).
- `--out` keeps each config's TLC output, the effective config (`<config>.effective.cfg`, the static config plus the facts) and `facts.cfg`. Without `--out`, the script makes a temporary folder and prints its path.

## Read the result

| Exit | Meaning | Next step |
| --- | --- | --- |
| 0 | Every config gave its expected result, and the covered functions match `fingerprint.txt`. | Done. |
| 1 | A property failed that holds at the sync commit. | The `rule:` line under the row names a broken invariant. For a property, TLC does not say which one broke: `/statechart` names it. Then follow "Bug or model error". |
| 2 | Setup error, or DRIFT: a line that the model depends on is gone or has a new form. | Install the missing tool, or update the model and `check.sh` for the new code shape. |
| 3 | TLC found nothing, but covered functions changed since the sync commit. This is not a pass: TLC checked the old model, and a change outside the facts is invisible to it. | Follow "Update the verification" before you trust the result. |

Read every TLC result with the `/statechart` skill. It is hidden from the default skill list, so load it by name. Its first run can install Java and TLC, which needs the network: its own instructions say how to use a TLC that you already have. Give it `NotificationBarrier.tla`, the `<config>.effective.cfg` from the output folder and the view module `statechart-view.mjs` in this folder. Work in a scratch folder outside the repository. For a failure, its report tells the shortest path to the broken rule in plain words. For a pass on `chart.cfg` (one update, one request), it draws how a request waits for an earlier handler. The other configs pass with too many states for a chart, and the report then lists the rules that hold. If `/statechart` rejects the view module because its interface changed, rewrite the module from `/statechart`'s instructions.

## Bug or model error

1. Tell the counterexample as steps with `/statechart`.
2. For each step, open the code in the mapping table, and check that the step can happen there in that order.
3. If one step cannot happen, the model is wrong. Fix the model, and say what was wrong.
4. If every step can happen, replay the path against the real code in a scratch `_test.go` file in the package, as `TestSendRequest_FromNotificationHandlerReturns` in `connection_notification_barrier_test.go` does for a request from a handler. Then delete the file. A failing replay is a bug in the code. Report it with the steps and the harm. Do not change the property or the model to make it pass.

## Update the verification

Do this after every change to covered code, on purpose or not, and always after exit 3:

1. Find each model action whose code changed, with the mapping table below. Edit the model from the code, not from the result. Keep one indivisible step of the code as one action: one critical section under `notifyMu`, one channel operation, or one `select`.
2. If you change a fact that `check.sh` reads, update its extraction and the facts table below.
3. Change a property only when the promise changed on purpose, and cite the doc comment, test or commit that says so.
4. Rerun `check.sh`. Then break the new code the way the property guards against, such as removing a check or reordering a step, and confirm that `check.sh` exits 1. Drop the break.
5. Run `check.sh --update-fingerprint` only after you confirm that the model describes the code. Commit `fingerprint.txt` in the same commit as the model change. That commit becomes the sync commit.

## Model to code

Lines are at the sync commit.

| Model action | Code in `connection.go` |
| --- | --- |
| `PeerSendsNote`, `PeerResponds`, `PeerCloses` | The peer writes a notification, a response or EOF to the stream. They have no fairness: the peer can stop at any time. In nested-handler.cfg (`NestedSpec`), the peer answers the handler's request in the end, or closes. Without that, the handler waits on the peer as a slow handler does, and so does a request that waits for that handler. |
| `RecvNotification` | `receive`, 503-527: `lastEnqueuedNotificationSeq++` and the non-blocking send under `notifyMu`. On a full queue: rollback (517), then `shutdownReceive(errNotificationQueueOverflow)`. |
| `RecvResponse` | `handleResponse`, 594-615: take `c.pending[idStr]` under `c.mu`, read the watermark under `notifyMu`. |
| `RecvDeliver` | `handleResponse`, 616: `pr.ch <- responseEnvelope{...}`. |
| `RecvEOF` | `receive`, 533-537: `scanner.Scan()` ends. |
| `ShutdownCancel` | `shutdownReceive`, 546: `c.cancel(cause)`. |
| `ShutdownClose` | `shutdownReceive`, 551-566: close the queue, read `finalEnqueuedSeq`, start the drain goroutine. One action, because only `receive` writes `lastEnqueuedNotificationSeq`. |
| `WorkerDequeue`, `WorkerExit` | `processNotifications`, 574: `for queued := range c.notificationQueue`. |
| `PlainHandlerReturns`, `SlowHandlerReturns`, `NestedHandlerCalls`, `NestedHandlerReturns` | `processNotifications`, 530-531: `c.handleInbound(handlerCtx, queued.msg)` runs the user's handler. `handlerCtx` is `c.inboundCtx` with a `handlingNotification` value that names this connection and `queued.seq`. Plain handlers return promptly (fair). Slow handlers return after any time (no fairness). A nested handler calls `SendRequest` on this goroutine with `handlerCtx`. |
| `WorkerComplete` | `processNotifications`, 578-590: `completedNotificationSeq = queued.seq` and `Broadcast` under `notifyMu`. |
| `AppCalls` | `SendRequest` 729-737, `SendRequestNoResult` 1053-1061: register `c.pending[idKey]`. The app decides when, so no fairness. |
| `SendsRequest` | `SendRequest` 739, `SendRequestNoResult` 1063: `c.sendMessage(msg)`. |
| `StartsPeerWait` | `SendRequest` 744 and `SendRequestNoResult` 1068: `startPeerWait(ctx)`, 967-990. For the ctx of this connection's running handler (`notificationSeq`, 59-65, called at 968), `c.peerWaits[seq]++` (978) and `Broadcast` (979) under `notifyMu`. Only the nested request has such a ctx, so app callers skip this step. |
| `EndsPeerWait` | `SendRequest` 746 and `SendRequestNoResult` 1070: `donePeerWait()` after `waitForResponse` returns, which decrements `c.peerWaits[seq]` under `notifyMu` (980-989). A request that recorded nothing gets a no-op func. |
| `GetsResponse`, `SeesCtxDone`, `CtxBranchChecksDone`, `SeesDisconnect`, `CleansUp` | `waitForResponse`, 843-874. `CleansUp` is `cleanupPending` (1044-1048). The disconnect checks read `c.Done()` (1121-1123). |
| `EntersBarrier` | `waitNotifications`, 885-915. `waitNotificationsUpTo` (876-878, called at 750 and 1074) calls it with `stopAtPeerWait` false, and `waitInboundBarrier` (955) with true. Return for target 0, else lock, cap the target at `h.seq-1` for the ctx of this connection's running handler (899-904, `CappedTarget`), and start the ctx waker. |
| `BarrierLoop` | `waitNotifications`, 917: the loop condition `completedNotificationSeq < target`, and with `stopAtPeerWait` also `!(c.peerWaits[c.completedNotificationSeq+1] > 0)` (`StopsAtPeerWait`). |
| `BarrierChecksDone` | `waitNotifications`, 922-926: `select { case <-c.Done(): ... default: }`. |
| `BarrierChecksCtxOrWaits` | `waitNotifications`, 927-945: the `ctx.Done()` select, then `notifyCond.Wait()`, which releases the lock and sleeps in one step. |
| `BarrierRelocks` | `waitNotifications`, 945: `Wait()` returns after a broadcast and takes the lock again. |
| `CtxWakerFires`, `CtxWakerStops` | `waitNotifications`, 906-915: the goroutine that broadcasts on `ctx.Done()`. |
| `ConnWatcherFires` | `NewConnection`, 161-166: the goroutine that broadcasts on `c.ctx.Done()`. |
| `DrainStarts` | `waitForNotificationDrain`, 1005-1031: return for target 0, else lock and start the timeout waker. |
| `DrainLoop` | `waitForNotificationDrain`, 1033-1036: the loop condition. |
| `DrainChecksTimeoutOrWaits` | `waitForNotificationDrain`, 1037-1040: `if ctx.Err() != nil { return }`, then `notifyCond.Wait()`. |
| `DrainRelocks` | `waitForNotificationDrain`, 1040: `Wait()` returns and takes the lock again. |
| `DrainWakerFires`, `DrainWakerStops` | `waitForNotificationDrain`, 1022-1031: the goroutine that broadcasts on the timeout. |
| `DrainTimerFires` | `context.WithTimeout`, 1010, with `notificationQueueDrainTimeout` (5 s, line 18). |
| `InboundCancel` | `shutdownReceive`, 565: `c.inboundCancel(cause)`. |
| `AppCancelsCtx` | The caller's ctx ends. No fairness. |
| `PeerSendsInReq`, `PeerCancelsInReq` | The peer writes a request or a `$/cancel_request` for it. No fairness. With `PeerNeedsInboundAnswer`, the peer answers the nested request only after our reply to its request. |
| `RecvRequest` | `receive`, 449-500: the watermark under `notifyMu` (455-457), `c.inflight[idKey] = cancel` under `c.mu` (467-470), then `go func`. The goroutine runs the caller actions above, from `EntersBarrier`, with its `reqCtx`. `reqCtx` derives from `c.ctx`, so a disconnect cancels it, and it carries no `handlingNotification`, so the cap does not apply. |
| `RecvCancel` | `receive`, 440-443, and `handleCancelRequest`, 620-645: look up `c.inflight[idKey]` under `c.mu`, then `cancel(context.Canceled)`. |
| `InAfterBarrier` | `receive`, 484, and `waitInboundBarrier`, 954-962: the wait's error, else the cancel error when `context.Cause(ctx)` is set (958), else nil. |
| `InChecksDone` | `receive`, 485-496: `select { case <-c.Done(): c.handleInbound(reqCtx, m) ... default: }`, else `c.sendInboundRequestError(m, toReqErr(err))` (688-697). The deferred `delete(c.inflight, idKey)` (474-483) runs after the reply. The model folds it into the reply step. |
| `InHandlerStarts` | `receive`, 498: `c.handleInbound(reqCtx, m)` after a nil error. A `$/cancel_request` or `EndsPeerWait` can run between `InAfterBarrier` and this step. |
| `InHandlerReturns` | `handleInbound`, 647-686: the request handler returns and the reply goes out. The model gives request handlers fairness. |

## Facts that check.sh reads

Each fact sets one model constant. The last column is the result of setting the fact to its broken value at the sync commit, which shows that each property can fail.

| Fact | Code at the sync commit | Broken value makes this fail |
| --- | --- | --- |
| `WaitsForBarrier` | `SendRequest` 750 and `SendRequestNoResult` 1074 call `waitNotificationsUpTo(ctx, resp.notificationWatermark)`, which is `waitNotifications(ctx, target, false)` (877) | `BarrierHolds` in barrier.cfg (the bug of #5) |
| `WatermarkSource` | `handleResponse` 610: `watermark := c.lastEnqueuedNotificationSeq` | `BarrierHolds` in barrier.cfg (with `completedNotificationSeq`) |
| `CompletesAfterHandler` | `processNotifications`: `handleInbound` (576) before `completedNotificationSeq = queued.seq` (584) | `InboundCtxOutlivesDrain` in barrier.cfg |
| `WorkerBroadcasts` | `processNotifications` 589: `Broadcast` after the count | `ResponseReturnsCaller` in barrier.cfg |
| `RollsBackOnOverflow` | `receive` 517: `lastEnqueuedNotificationSeq--` | `NoDeadlock` in barrier.cfg: the drain waits for a notification that was never queued |
| `DrainBeforeInboundCancel` | `shutdownReceive` 564-565 | `InboundCtxOutlivesDrain` in barrier.cfg |
| `CtxWakerSpawned` | `waitNotifications` 906-915 | `CancelReturnsCaller` in slow-handler.cfg |
| `ConnDoneWaker` | `NewConnection` 161-166 | `DisconnectReturnsCaller` in slow-handler.cfg |
| `DrainTimerWaker` | `waitForNotificationDrain` 1022-1031 | `ShutdownCompletes` in drain-timeout.cfg |
| `WaitChecksDisconnect` | `waitNotifications` 922-926: `case <-c.Done():` before the loop's `case <-ctx.Done():` | `DisconnectReturnsCaller` in slow-handler.cfg |
| `DrainChecksTimeout` | `waitForNotificationDrain` 1037: `if ctx.Err() != nil {` before `Wait()` | `ShutdownCompletes` in drain-timeout.cfg |
| `InboundWaits` | `receive` 456: `watermark := c.lastEnqueuedNotificationSeq`, and 484: `if err := c.waitInboundBarrier(reqCtx, watermark); err != nil {` | `InboundHandlerAfterNotes` in inbound-request.cfg: the permission prompt shows before its tool call |
| `InboundStopsAtPeerWait` | `waitInboundBarrier` 955 calls `waitNotifications(ctx, watermark, true)`, and the loop at 917 checks `c.peerWaits[c.completedNotificationSeq+1]` | `InboundRequestAnswered` in inbound-nested-peer.cfg: the handler and the peer's request wait for each other |
| `PeerWaitCounts` | `startPeerWait` 978: `c.peerWaits[seq]++`, with `startPeerWait(ctx)` before `waitForResponse` in `SendRequest` (744) and `SendRequestNoResult` (1068) | `InboundRequestAnswered` in inbound-nested-peer.cfg |
| `PeerWaitBroadcasts` | `startPeerWait` 979: `Broadcast` after the count | `InboundRequestAnswered` in inbound-nested-peer.cfg: a request that already sleeps never sees the count |
| `PeerWaitEnds` | `startPeerWait` 986: the decrement, with `donePeerWait()` after `waitForResponse` (746, 1070) | `InboundHandlerAfterNotes` in inbound-nested.cfg: a request starts after the handler's request returned, while the handler still runs |
| `PostWaitChecksCtx` | `waitInboundBarrier` 958: `if cause := context.Cause(ctx); cause != nil {` after the wait | `CancelBeforeCheckSkipsHandler` in inbound-request.cfg: a `$/cancel_request` that lands as the wait clears still runs the handler |
| `HandlerCtxCapsWait` | `processNotifications` 575-576 passes `handlerCtx`, and `waitNotifications` 902-903 sets `target = min(target, h.seq-1)` before the loop | `ResponseReturnsCaller` in nested-handler.cfg: a handler's request on the same connection never returns, and the client handles no later notification |

`check.sh` also refuses to run (DRIFT, exit 2) when the code no longer has the shape that the model assumes: one `receive` goroutine, one `processNotifications` goroutine, handlers that get `c.inboundCtx` or a `handlerCtx` made from it, handlers and responses handled inline, and a non-blocking queue send. A new form of the cap in `waitNotifications`, a `waitNotificationsUpTo` that is not `waitNotifications(ctx, target, false)`, and a `startPeerWait` that does not find the running handler with `notificationSeq(ctx)` are DRIFT too.

## Bounds and assumptions

- barrier.cfg: 3 notifications, queue capacity 2, one app caller, ctx cancel and EOF allowed, 48,455 states.
- slow-handler.cfg: 2 notifications, plain and slow handlers, no drain timeout, 36,602 states.
- drain-timeout.cfg: as slow-handler, and the timeout fires in the end, 83,448 states.
- nested-handler.cfg: 2 notifications, one of them with a handler that calls a request with its ctx. The peer answers that request in the end, or closes (`NestedSpec`). 183,065 states.
- deep-two-callers.cfg (`--deep`): 2 notifications, two app callers, 1,127,364 states.
- inbound-request.cfg: 2 notifications, one request from the peer, `$/cancel_request` and EOF allowed, no app caller, 19,895 states.
- inbound-slow-handler.cfg: as inbound-request, with plain and slow handlers, 51,756 states.
- inbound-nested.cfg: 2 notifications, plain and nested handlers, one request from the peer, a peer that answers the nested request in the end (`NestedSpec`), 102,633 states.
- inbound-nested-peer.cfg: 1 notification with a nested handler, one request from the peer, and a peer that answers the nested request only after our reply (`NestedSpec`), 5,386 states.
- The real queue holds 1024 notifications. The model uses 2, and overflow takes the same code path.
- Time is not modeled. "5 s" means "in the end", and fairness gives each goroutine its turn.
- Mutexes, channels and `sync.Cond` act as the Go documentation says. `Wait` has no spurious wakeups.
- A nested handler makes one request. Only that request counts in `c.peerWaits`.
- A request from a handler uses that handler's ctx. The model has one connection, and it does not model a ctx that another connection's handler passes in, a ctx that outlives its handler, or a request from a handler with an unrelated ctx. `TestSendRequest_WithOtherHandlerCtxWaitsForPreResponseNotification` covers the first two. Both keep the full wait. The `SendRequest` doc comment says that the third waits for the handler that made it.

Not covered: a goroutine other than the handler that makes a request with a running handler's ctx (the code caps its wait too, by design, and the `SendRequest` doc comment says so), JSON parsing, request ID canonicalization (`canonicalJSONRPCIDKey`), request IDs (`prepareRequest`), the error that `toReqErr` in `errors.go` builds, the `$/cancel_request` that we send (`sendCancelRequests`), `sendMessage` (the model takes every write as done, and write errors are not modeled), a request id that the peer reuses while its first request is still in `c.inflight`, a request handler that itself calls a request (app callers stand for it), the in-flight watermark counter (`inflightWatermarks`, `oldestInflightWatermark`) and the `handlingRequest` ctx value, and the generated handlers in `agent_gen.go` and `client_gen.go`, such as the `sessionCancels` map and the session/cancel markers in `agent.go`. A pass covers the model within these bounds, not the whole SDK.

## Files

- `NotificationBarrier.tla`: the model and the properties.
- `barrier.cfg`, `slow-handler.cfg`, `drain-timeout.cfg`, `nested-handler.cfg`, `inbound-request.cfg`, `inbound-slow-handler.cfg`, `inbound-nested.cfg`, `inbound-nested-peer.cfg`: the configs that `check.sh` runs. `deep-two-callers.cfg` runs with `--deep`.
- `chart.cfg`: a small config for a `/statechart` chart. `check.sh` does not run it. Append `facts.cfg` from a `check.sh` output folder to it before you use it.
- `statechart-view.mjs`: labels for `/statechart`.
- `check.sh`: reads the facts, runs TLC, compares `fingerprint.txt`.
- `fingerprint.txt`: sha256 of each covered function at the sync commit.

## Coverage record

- File: `connection.go`.
- Functions: `NewConnection`, `receive`, `handleCancelRequest`, `shutdownReceive`, `processNotifications`, `handleResponse`, `SendRequest`, `SendRequestNoResult`, `waitForResponse`, `waitNotificationsUpTo`, `waitNotifications`, `waitInboundBarrier`, `startPeerWait`, `waitForNotificationDrain`, and the helpers that the model relies on: `notificationSeq`, `handleInbound`, `sendInboundRequestError`, `cleanupPending` and `Done`.
- Sync commit: the last commit that changed `fingerprint.txt`, which `git log -1 --format=%H -- .agents/skills/verify-notification-barrier/fingerprint.txt` prints. The model was last confirmed to describe these functions at that commit, and the line numbers in this file are at that commit. A squash merge keeps the rule true, because the merged commit changes `fingerprint.txt` too.
- To see what changed since then: `git diff "$(git log -1 --format=%H -- .agents/skills/verify-notification-barrier/fingerprint.txt)" -- connection.go`. `check.sh` exit 3 names the changed functions.
