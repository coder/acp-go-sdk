---
name: verify-notification-barrier
description: Model-check the notification barrier in connection.go with TLA+ (TLC). The barrier makes SendRequest (and so Prompt and every other request) return only after the handlers of all notifications that arrived before its response have finished, and makes shutdown drain those handlers. It also makes the handler of a request from the peer start only after the handlers of notifications read before that request. Use before you finish any change to NewConnection, receive, handleCancelRequest, shutdownReceive, processNotifications, handleResponse, SendRequest, SendRequestNoResult, waitForResponse, waitNotificationsUpTo or waitForNotificationDrain, and when a bug report says that Prompt returned before its session/update handlers finished, that a request hangs, or that a handler context ended too early.
---

# Verify the notification barrier

`connection.go` has one reader goroutine, one goroutine that runs notification handlers in order, any number of goroutines that wait in `SendRequest`, and one goroutine for each request from the peer. Four fixes already went into this code (#5, #8, #10, #30), and unit tests reach few of its interleavings. For example, `go test ./...` still passes when `SendRequestNoResult` stops waiting for earlier handlers, when the wait loop stops checking for a disconnect, or when the drain loop stops checking its timeout. `check.sh` fails on all three. This skill checks every interleaving of a small model of that code with TLC.

The model is a hand translation. `check.sh` reads thirteen facts from `connection.go` on every run and sets the model from them, so a change to one of those lines changes what TLC checks. Any other change inside a covered function makes `check.sh` report STALE until you review the model against the code.

## What it proves

Each property is one promise to SDK users. The config names show where TLC checks it.

| Property | Promise | Harm if broken | Configs |
| --- | --- | --- | --- |
| `BarrierHolds` | When `SendRequest` returns a response, the handler of every notification that the peer sent before that response has returned. A request that a handler makes with its own ctx waits only for the notifications before that handler's own, as the `SendRequest` doc comment says. | `Prompt()` returns while `session/update` handlers still run, so a client renders the end of a turn before its last chunks. | all |
| `InboundCtxOutlivesDrain` | After a disconnect, handlers of notifications that were already read keep a live ctx until they return, or until the 5 s drain timeout. | Handlers that respect ctx drop the last messages of a session. | all |
| `ResponseReturnsCaller` | Once the response is read, the caller returns. It needs no disconnect, no ctx cancel and no timeout. This includes a request that a notification handler makes on the same connection with its own ctx. The configs that check it have only handlers that return in the end, so nested-handler.cfg assumes that the peer answers the handler's request in the end. | A request hangs although its answer arrived. When a handler's request hangs, the client also handles no later notification until the peer exits (`TestSendRequest_FromNotificationHandlerReturns`). | barrier, deep, nested-handler |
| `CancelReturnsCaller` | A canceled ctx makes the caller return, also while a handler that it waits for never finishes. For a request from the peer, `$/cancel_request` is the cancel. | Cancel does not stop a stuck request. | slow-handler, inbound-slow-handler |
| `DisconnectReturnsCaller` | A disconnect makes the caller return at once, also while a handler that it waits for never finishes. | Callers hang after the peer exits. | slow-handler, inbound-slow-handler |
| `InboundHandlerAfterNotes` | While the connection is up, the handler of a request from the peer starts only after the handler of every notification read before that request has returned. | The client shows the permission prompt before the tool call that it asks about. | inbound-* |
| `NoWatermarkPanic` | No wait target exceeds `lastEnqueuedNotificationSeq`, so the panics in `waitNotificationsUpTo` cannot fire. | The SDK panics. | inbound-* |
| `InboundHandlerStarts` | The handler of a request from the peer starts when the earlier handlers return, unless the peer canceled the request first. | A request from the peer hangs. | inbound-request |
| `InboundRequestAnswered` | Every request from the peer gets one reply: the handler's result or the cancel error. | The peer waits forever. | inbound-request, inbound-nested-peer (known finding) |
| `ShutdownCompletes` | After a disconnect, handler contexts end in the end. | Goroutines and handler work leak after every session. | barrier, drain-timeout, deep |
| `NoDeadlock` (`CHECK_DEADLOCK`) | No state exists where every goroutine is stuck and nothing can move. | The connection freezes. | all |

After a disconnect, the handler of a request from the peer starts with a canceled ctx, also while an earlier notification handler still runs. The code comment in `receive` says so, and no property promises the order then.

A slow notification handler delays every later request from the peer until it returns, the peer cancels the request, or the peer disconnects. That is the price of the order. `inbound-slow-handler.cfg` does not check `InboundHandlerStarts`, because the slow handler makes it fail.

## Known finding

`inbound-nested-peer.cfg` fails on the current code, and `check.sh` expects that failure.

- Rule: `InboundRequestAnswered`.
- In one sentence: when a notification handler waits for the peer's answer, and the peer answers only after we answer its own request, both wait forever.
- Steps, in the code's words:
  1. The peer sends a `session/update`. `receive` gives it sequence 1 and queues it.
  2. `processNotifications` calls the handler with `handlerCtx`. The handler calls a request on the same connection with that ctx, such as `CallExtension`. `waitForResponse` waits for the peer.
  3. Before it answers, the peer sends its own request. `receive` reads `watermark := c.lastEnqueuedNotificationSeq`, which is 1, and starts the request goroutine.
  4. The goroutine calls `waitNotificationsUpTo(reqCtx, 1)`. `reqCtx` has no `handlingNotification` value, so the cap does not apply. The goroutine sleeps, because `completedNotificationSeq` is 0.
  5. The peer answers step 2 only after our reply to step 3. That reply waits for step 2's handler to return. Nothing moves until the peer closes the stream or sends `$/cancel_request`.
- Harm: the handler, the peer's request and every later notification hang. Before `receive` waited for earlier handlers, step 4 started the request handler at once, and the cycle did not exist.
- Reach: the peer must make its answer depend on its own request to us, for example in an extension method. No pair of core ACP methods does this.

When a fix lands, `inbound-nested-peer.cfg` passes, and `check.sh` exits 1 with "passed, but it should fail". Then change its expected result in `check.sh` from `13` to `0`, and move this section into the property table.

## When to run it

Run it when `git diff` touches any covered function (see the coverage record at the end), or when the code that you changed writes `lastEnqueuedNotificationSeq`, `completedNotificationSeq`, `notificationQueue`, `pending`, `notifyCond`, `inboundCtx`, `c.ctx`, `c.inflight` or the handler ctx value `handlingNotification`. Run it once before your change, so that you know the starting result, and once after it.

## Run

From the repository root:

```bash
.agents/skills/verify-notification-barrier/check.sh --out "$OUT_DIR"
```

- It needs Java 11 or newer, `curl`, `sha256sum` and `awk`.
- The first run downloads `tla2tools.jar` 1.7.4 into `${ACP_TLA_CACHE:-${XDG_CACHE_HOME:-~/.cache}/acp-go-sdk-tla}` and checks its sha256 on every run. To use a copy that you already have, set `TLA2TOOLS_JAR`. To pick a JVM, set `JAVA`. The default heap is 4 GB: set `TLC_HEAP` to change it.
- It takes about 1.5 minutes. `--deep` adds `deep-two-callers.cfg`: two app goroutines call at once, which takes about 15 more minutes (1,127,364 states at the sync commit).
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
| `RecvNotification` | `receive`, 458-482: `lastEnqueuedNotificationSeq++` and the non-blocking send under `notifyMu`. On a full queue: rollback (472), then `shutdownReceive(errNotificationQueueOverflow)`. |
| `RecvResponse` | `handleResponse`, 549-570: take `c.pending[idStr]` under `c.mu`, read the watermark under `notifyMu`. |
| `RecvDeliver` | `handleResponse`, 571: `pr.ch <- responseEnvelope{...}`. |
| `RecvEOF` | `receive`, 488-492: `scanner.Scan()` ends. |
| `ShutdownCancel` | `shutdownReceive`, 501: `c.cancel(cause)`. |
| `ShutdownClose` | `shutdownReceive`, 506-521: close the queue, read `finalEnqueuedSeq`, start the drain goroutine. One action, because only `receive` writes `lastEnqueuedNotificationSeq`. |
| `WorkerDequeue`, `WorkerExit` | `processNotifications`, 529: `for queued := range c.notificationQueue`. |
| `PlainHandlerReturns`, `SlowHandlerReturns`, `NestedHandlerCalls`, `NestedHandlerReturns` | `processNotifications`, 530-531: `c.handleInbound(handlerCtx, queued.msg)` runs the user's handler. `handlerCtx` is `c.inboundCtx` with a `handlingNotification` value that names this connection and `queued.seq`. Plain handlers return promptly (fair). Slow handlers return after any time (no fairness). A nested handler calls `SendRequest` on this goroutine with `handlerCtx`. |
| `WorkerComplete` | `processNotifications`, 533-545: `completedNotificationSeq = queued.seq` and `Broadcast` under `notifyMu`. |
| `AppCalls` | `SendRequest` 681-689, `SendRequestNoResult` 939-947: register `c.pending[idKey]`. The app decides when, so no fairness. |
| `SendsRequest` | `SendRequest` 691, `SendRequestNoResult` 949: `c.sendMessage(msg)`. |
| `GetsResponse`, `SeesCtxDone`, `CtxBranchChecksDone`, `SeesDisconnect`, `CleansUp` | `waitForResponse`, 793-823. |
| `EntersBarrier` | `waitNotificationsUpTo`, 826-856, called at 700 and 958: return for target 0, else lock, cap the target at `h.seq-1` for the ctx of this connection's running handler (840-845, `CappedTarget`), and start the ctx waker. |
| `BarrierLoop` | `waitNotificationsUpTo`, 858-861: the loop condition `completedNotificationSeq < target`. |
| `BarrierChecksDone` | `waitNotificationsUpTo`, 863-867: `select { case <-c.Done(): ... default: }`. |
| `BarrierChecksCtxOrWaits` | `waitNotificationsUpTo`, 868-886: the `ctx.Done()` select, then `notifyCond.Wait()`, which releases the lock and sleeps in one step. |
| `BarrierRelocks` | `waitNotificationsUpTo`, 886: `Wait()` returns after a broadcast and takes the lock again. |
| `CtxWakerFires`, `CtxWakerStops` | `waitNotificationsUpTo`, 847-856: the goroutine that broadcasts on `ctx.Done()`. |
| `ConnWatcherFires` | `NewConnection`, 123-128: the goroutine that broadcasts on `c.ctx.Done()`. |
| `DrainStarts` | `waitForNotificationDrain`, 892-917: return for target 0, else lock and start the timeout waker. |
| `DrainLoop` | `waitForNotificationDrain`, 919-922: the loop condition. |
| `DrainChecksTimeoutOrWaits` | `waitForNotificationDrain`, 923-926: `if ctx.Err() != nil { return }`, then `notifyCond.Wait()`. |
| `DrainRelocks` | `waitForNotificationDrain`, 926: `Wait()` returns and takes the lock again. |
| `DrainWakerFires`, `DrainWakerStops` | `waitForNotificationDrain`, 908-917: the goroutine that broadcasts on the timeout. |
| `DrainTimerFires` | `context.WithTimeout`, 896, with `notificationQueueDrainTimeout` (5 s, line 18). |
| `InboundCancel` | `shutdownReceive`, 520: `c.inboundCancel(cause)`. |
| `AppCancelsCtx` | The caller's ctx ends. No fairness. |
| `PeerSendsInReq`, `PeerCancelsInReq` | The peer writes a request or a `$/cancel_request` for it. No fairness. With `PeerNeedsInboundAnswer`, the peer answers the nested request only after our reply to its request. |
| `RecvRequest` | `receive`, 411-455: the watermark under `notifyMu` (415-417), `c.inflight[idKey] = cancel` under `c.mu` (426-428), then `go func`. The goroutine runs the caller actions above, from `EntersBarrier`, with its `reqCtx`. `reqCtx` derives from `c.ctx`, so a disconnect cancels it, and it carries no `handlingNotification`, so the cap does not apply. |
| `RecvCancel` | `receive`, 402-405, and `handleCancelRequest`, 575-600: look up `c.inflight[idKey]` under `c.mu`, then `cancel(context.Canceled)`. |
| `InAfterBarrier` | `receive`, 439 and 453: a nil error from `waitNotificationsUpTo` starts `c.handleInbound(reqCtx, m)`. |
| `InChecksDone` | `receive`, 440-451: `select { case <-c.Done(): c.handleInbound(reqCtx, m) ... default: }`, else `c.sendInboundRequestError(m, toReqErr(err))`. The deferred `delete(c.inflight, idKey)` (432-438) runs after the reply. The model folds it into the reply step. |
| `InHandlerReturns` | `handleInbound`, 602-641: the request handler returns and the reply goes out. The model gives request handlers fairness. |

## Facts that check.sh reads

Each fact sets one model constant. The last column is the result of setting the fact to its broken value at the sync commit, which shows that each property can fail.

| Fact | Code at the sync commit | Broken value makes this fail |
| --- | --- | --- |
| `WaitsForBarrier` | `SendRequest` 700 and `SendRequestNoResult` 958 call `waitNotificationsUpTo(ctx, resp.notificationWatermark)` | `BarrierHolds` in barrier.cfg (the bug of #5) |
| `WatermarkSource` | `handleResponse` 565: `watermark := c.lastEnqueuedNotificationSeq` | `BarrierHolds` in barrier.cfg (with `completedNotificationSeq`) |
| `CompletesAfterHandler` | `processNotifications`: `handleInbound` (531) before `completedNotificationSeq = queued.seq` (539) | `InboundCtxOutlivesDrain` in barrier.cfg |
| `WorkerBroadcasts` | `processNotifications` 544: `Broadcast` after the count | `ResponseReturnsCaller` in barrier.cfg |
| `RollsBackOnOverflow` | `receive` 472: `lastEnqueuedNotificationSeq--` | `NoDeadlock` in barrier.cfg: the drain waits for a notification that was never queued |
| `DrainBeforeInboundCancel` | `shutdownReceive` 519-520 | `InboundCtxOutlivesDrain` in barrier.cfg |
| `CtxWakerSpawned` | `waitNotificationsUpTo` 847-856 | `CancelReturnsCaller` in slow-handler.cfg |
| `ConnDoneWaker` | `NewConnection` 123-128 | `DisconnectReturnsCaller` in slow-handler.cfg |
| `DrainTimerWaker` | `waitForNotificationDrain` 908-917 | `ShutdownCompletes` in drain-timeout.cfg |
| `WaitChecksDisconnect` | `waitNotificationsUpTo` 863-867: `case <-c.Done():` before the loop's `case <-ctx.Done():` | `DisconnectReturnsCaller` in slow-handler.cfg |
| `DrainChecksTimeout` | `waitForNotificationDrain` 923: `if ctx.Err() != nil {` before `Wait()` | `ShutdownCompletes` in drain-timeout.cfg |
| `InboundWaits` | `receive` 416: `watermark := c.lastEnqueuedNotificationSeq`, and 439: `if err := c.waitNotificationsUpTo(reqCtx, watermark); err != nil {` | `InboundHandlerAfterNotes` in inbound-request.cfg: the permission prompt shows before its tool call |
| `HandlerCtxCapsWait` | `processNotifications` 530-531 passes `handlerCtx`, and `waitNotificationsUpTo` 843-844 sets `target = min(target, h.seq-1)` before the loop | `ResponseReturnsCaller` in nested-handler.cfg: a handler's request on the same connection never returns, and the client handles no later notification |

`check.sh` also refuses to run (DRIFT, exit 2) when the code no longer has the shape that the model assumes: one `receive` goroutine, one `processNotifications` goroutine, handlers that get `c.inboundCtx` or a `handlerCtx` made from it, handlers and responses handled inline, and a non-blocking queue send. A new form of the cap in `waitNotificationsUpTo` is DRIFT too.

## Bounds and assumptions

- barrier.cfg: 3 notifications, queue capacity 2, one app caller, ctx cancel and EOF allowed, 48,455 states.
- slow-handler.cfg: 2 notifications, plain and slow handlers, no drain timeout, 36,602 states.
- drain-timeout.cfg: as slow-handler, and the timeout fires in the end, 83,448 states.
- nested-handler.cfg: 2 notifications, one of them with a handler that calls a request with its ctx. The peer answers that request in the end, or closes (`NestedSpec`). 155,948 states.
- deep-two-callers.cfg (`--deep`): 2 notifications, two app callers, 1,127,364 states.
- inbound-request.cfg: 2 notifications, one request from the peer, `$/cancel_request` and EOF allowed, no app caller, 15,742 states.
- inbound-slow-handler.cfg: as inbound-request, with plain and slow handlers, 42,022 states.
- inbound-nested-peer.cfg: 1 notification with a nested handler, one request from the peer, and a peer that answers the nested request only after our reply (`NestedSpec`), 2,652 states.
- The real queue holds 1024 notifications. The model uses 2, and overflow takes the same code path.
- Time is not modeled. "5 s" means "in the end", and fairness gives each goroutine its turn.
- Mutexes, channels and `sync.Cond` act as the Go documentation says. `Wait` has no spurious wakeups.
- A request from a handler uses that handler's ctx. The model has one connection, and it does not model a ctx that another connection's handler passes in, a ctx that outlives its handler, or a request from a handler with an unrelated ctx. `TestSendRequest_WithOtherHandlerCtxWaitsForPreResponseNotification` covers the first two. Both keep the full wait. The `SendRequest` doc comment says that the third waits for the handler that made it.

Not covered: a goroutine other than the handler that makes a request with a running handler's ctx (the code caps its wait too, by design, and the `SendRequest` doc comment says so), JSON parsing, request ID canonicalization, the `$/cancel_request` that we send (`sendCancelRequests`), write errors from `sendMessage`, a request id that the peer reuses while its first request is still in `c.inflight`, a request handler that itself calls a request (app callers stand for it), and the generated handlers in `agent_gen.go` and `client_gen.go`, such as the `sessionCancels` map. A pass covers the model within these bounds, not the whole SDK.

## Files

- `NotificationBarrier.tla`: the model and the properties.
- `barrier.cfg`, `slow-handler.cfg`, `drain-timeout.cfg`, `nested-handler.cfg`, `inbound-request.cfg`, `inbound-slow-handler.cfg`, `inbound-nested-peer.cfg`: the configs that `check.sh` runs. `deep-two-callers.cfg` runs with `--deep`.
- `chart.cfg`: a small config for a `/statechart` chart. `check.sh` does not run it. Append `facts.cfg` from a `check.sh` output folder to it before you use it.
- `statechart-view.mjs`: labels for `/statechart`.
- `check.sh`: reads the facts, runs TLC, compares `fingerprint.txt`.
- `fingerprint.txt`: sha256 of each covered function at the sync commit.

## Coverage record

- File: `connection.go`.
- Functions: `NewConnection`, `receive`, `handleCancelRequest`, `shutdownReceive`, `processNotifications`, `handleResponse`, `SendRequest`, `SendRequestNoResult`, `waitForResponse`, `waitNotificationsUpTo`, `waitForNotificationDrain`.
- Sync commit: the last commit that changed `fingerprint.txt`, which `git log -1 --format=%H -- .agents/skills/verify-notification-barrier/fingerprint.txt` prints. The model was last confirmed to describe these functions at that commit, and the line numbers in this file are at that commit. A squash merge keeps the rule true, because the merged commit changes `fingerprint.txt` too.
- To see what changed since then: `git diff "$(git log -1 --format=%H -- .agents/skills/verify-notification-barrier/fingerprint.txt)" -- connection.go`. `check.sh` exit 3 names the changed functions.
