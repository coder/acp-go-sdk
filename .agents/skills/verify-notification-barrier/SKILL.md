---
name: verify-notification-barrier
description: Model-check the notification barrier in connection.go with TLA+ (TLC). The barrier makes SendRequest (and so Prompt and every other request) return only after the handlers of all notifications that arrived before its response have finished, and makes shutdown drain those handlers. Use before you finish any change to NewConnection, receive, shutdownReceive, processNotifications, handleResponse, SendRequest, SendRequestNoResult, waitForResponse, waitNotificationsUpTo or waitForNotificationDrain, and when a bug report says that Prompt returned before its session/update handlers finished, that a request hangs, or that a handler context ended too early.
---

# Verify the notification barrier

`connection.go` has one reader goroutine, one goroutine that runs notification handlers in order, and any number of goroutines that wait in `SendRequest`. Four fixes already went into this code (#5, #8, #10, #30), and unit tests reach few of its interleavings. For example, `go test ./...` still passes when `SendRequestNoResult` stops waiting for earlier handlers, when the wait loop stops checking for a disconnect, or when the drain loop stops checking its timeout. `check.sh` fails on all three. This skill checks every interleaving of a small model of that code with TLC.

The model is a hand translation. `check.sh` reads twelve facts from `connection.go` on every run and sets the model from them, so a change to one of those lines changes what TLC checks. Any other change inside a covered function makes `check.sh` report STALE until you review the model against the code.

## What it proves

Each property is one promise to SDK users. The config names show where TLC checks it.

| Property | Promise | Harm if broken | Configs |
| --- | --- | --- | --- |
| `BarrierHolds` | When `SendRequest` returns a response, the handler of every notification that the peer sent before that response has returned. A request that a handler makes with its own ctx waits only for the notifications before that handler's own, as the `SendRequest` doc comment says. | `Prompt()` returns while `session/update` handlers still run, so a client renders the end of a turn before its last chunks. | all |
| `InboundCtxOutlivesDrain` | After a disconnect, handlers of notifications that were already read keep a live ctx until they return, or until the 5 s drain timeout. | Handlers that respect ctx drop the last messages of a session. | all |
| `ResponseReturnsCaller` | Once the response is read, the caller returns. It needs no disconnect, no ctx cancel and no timeout. This includes a request that a notification handler makes on the same connection with its own ctx. The configs that check it have only handlers that return in the end, so nested-handler.cfg assumes that the peer answers the handler's request in the end. | A request hangs although its answer arrived. When a handler's request hangs, the client also handles no later notification until the peer exits (`TestSendRequest_FromNotificationHandlerReturns`). | barrier, deep, nested-handler |
| `CancelReturnsCaller` | A canceled ctx makes the caller return, also while a handler that it waits for never finishes. | Cancel does not stop a stuck request. | slow-handler |
| `DisconnectReturnsCaller` | A disconnect makes the caller return at once, also while a handler that it waits for never finishes. | Callers hang after the peer exits. | slow-handler |
| `ShutdownCompletes` | After a disconnect, handler contexts end in the end. | Goroutines and handler work leak after every session. | barrier, drain-timeout, deep |
| `NoDeadlock` (`CHECK_DEADLOCK`) | No state exists where every goroutine is stuck and nothing can move. | The connection freezes. | all |

## When to run it

Run it when `git diff` touches any covered function (see the coverage record at the end), or when the code that you changed writes `lastEnqueuedNotificationSeq`, `completedNotificationSeq`, `notificationQueue`, `pending`, `notifyCond`, `inboundCtx`, `c.ctx` or the handler ctx value `handlingNotification`. Run it once before your change, so that you know the starting result, and once after it.

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
| `RecvNotification` | `receive`, 437-461: `lastEnqueuedNotificationSeq++` and the non-blocking send under `notifyMu`. On a full queue: rollback (451), then `shutdownReceive(errNotificationQueueOverflow)`. |
| `RecvResponse` | `handleResponse`, 528-549: take `c.pending[idStr]` under `c.mu`, read the watermark under `notifyMu`. |
| `RecvDeliver` | `handleResponse`, 550: `pr.ch <- responseEnvelope{...}`. |
| `RecvEOF` | `receive`, 467-471: `scanner.Scan()` ends. |
| `ShutdownCancel` | `shutdownReceive`, 480: `c.cancel(cause)`. |
| `ShutdownClose` | `shutdownReceive`, 485-500: close the queue, read `finalEnqueuedSeq`, start the drain goroutine. One action, because only `receive` writes `lastEnqueuedNotificationSeq`. |
| `WorkerDequeue`, `WorkerExit` | `processNotifications`, 508: `for queued := range c.notificationQueue`. |
| `PlainHandlerReturns`, `SlowHandlerReturns`, `NestedHandlerCalls`, `NestedHandlerReturns` | `processNotifications`, 509-510: `c.handleInbound(handlerCtx, queued.msg)` runs the user's handler. `handlerCtx` is `c.inboundCtx` with a `handlingNotification` value that names this connection and `queued.seq`. Plain handlers return promptly (fair). Slow handlers return after any time (no fairness). A nested handler calls `SendRequest` on this goroutine with `handlerCtx`. |
| `WorkerComplete` | `processNotifications`, 512-524: `completedNotificationSeq = queued.seq` and `Broadcast` under `notifyMu`. |
| `AppCalls` | `SendRequest` 649-657, `SendRequestNoResult` 907-915: register `c.pending[idKey]`. The app decides when, so no fairness. |
| `SendsRequest` | `SendRequest` 659, `SendRequestNoResult` 917: `c.sendMessage(msg)`. |
| `GetsResponse`, `SeesCtxDone`, `CtxBranchChecksDone`, `SeesDisconnect`, `CleansUp` | `waitForResponse`, 761-791. |
| `EntersBarrier` | `waitNotificationsUpTo`, 794-824, called at 668 and 926: return for target 0, else lock, cap the target at `h.seq-1` for the ctx of this connection's running handler (808-813, `CappedTarget`), and start the ctx waker. |
| `BarrierLoop` | `waitNotificationsUpTo`, 826-829: the loop condition `completedNotificationSeq < target`. |
| `BarrierChecksDone` | `waitNotificationsUpTo`, 831-835: `select { case <-c.Done(): ... default: }`. |
| `BarrierChecksCtxOrWaits` | `waitNotificationsUpTo`, 836-854: the `ctx.Done()` select, then `notifyCond.Wait()`, which releases the lock and sleeps in one step. |
| `BarrierRelocks` | `waitNotificationsUpTo`, 854: `Wait()` returns after a broadcast and takes the lock again. |
| `CtxWakerFires`, `CtxWakerStops` | `waitNotificationsUpTo`, 815-824: the goroutine that broadcasts on `ctx.Done()`. |
| `ConnWatcherFires` | `NewConnection`, 123-128: the goroutine that broadcasts on `c.ctx.Done()`. |
| `DrainStarts` | `waitForNotificationDrain`, 860-885: return for target 0, else lock and start the timeout waker. |
| `DrainLoop` | `waitForNotificationDrain`, 887-890: the loop condition. |
| `DrainChecksTimeoutOrWaits` | `waitForNotificationDrain`, 891-894: `if ctx.Err() != nil { return }`, then `notifyCond.Wait()`. |
| `DrainRelocks` | `waitForNotificationDrain`, 894: `Wait()` returns and takes the lock again. |
| `DrainWakerFires`, `DrainWakerStops` | `waitForNotificationDrain`, 876-885: the goroutine that broadcasts on the timeout. |
| `DrainTimerFires` | `context.WithTimeout`, 864, with `notificationQueueDrainTimeout` (5 s, line 18). |
| `InboundCancel` | `shutdownReceive`, 499: `c.inboundCancel(cause)`. |
| `AppCancelsCtx` | The caller's ctx ends. No fairness. |

## Facts that check.sh reads

Each fact sets one model constant. The last column is the result of setting the fact to its broken value at the sync commit, which shows that each property can fail.

| Fact | Code at the sync commit | Broken value makes this fail |
| --- | --- | --- |
| `WaitsForBarrier` | `SendRequest` 668 and `SendRequestNoResult` 926 call `waitNotificationsUpTo(ctx, resp.notificationWatermark)` | `BarrierHolds` in barrier.cfg (the bug of #5) |
| `WatermarkSource` | `handleResponse` 544: `watermark := c.lastEnqueuedNotificationSeq` | `BarrierHolds` in barrier.cfg (with `completedNotificationSeq`) |
| `CompletesAfterHandler` | `processNotifications`: `handleInbound` (510) before `completedNotificationSeq = queued.seq` (518) | `InboundCtxOutlivesDrain` in barrier.cfg |
| `WorkerBroadcasts` | `processNotifications` 523: `Broadcast` after the count | `ResponseReturnsCaller` in barrier.cfg |
| `RollsBackOnOverflow` | `receive` 451: `lastEnqueuedNotificationSeq--` | `NoDeadlock` in barrier.cfg: the drain waits for a notification that was never queued |
| `DrainBeforeInboundCancel` | `shutdownReceive` 498-499 | `InboundCtxOutlivesDrain` in barrier.cfg |
| `CtxWakerSpawned` | `waitNotificationsUpTo` 815-824 | `CancelReturnsCaller` in slow-handler.cfg |
| `ConnDoneWaker` | `NewConnection` 123-128 | `DisconnectReturnsCaller` in slow-handler.cfg |
| `DrainTimerWaker` | `waitForNotificationDrain` 876-885 | `ShutdownCompletes` in drain-timeout.cfg |
| `WaitChecksDisconnect` | `waitNotificationsUpTo` 831-835: `case <-c.Done():` before the loop's `case <-ctx.Done():` | `DisconnectReturnsCaller` in slow-handler.cfg |
| `DrainChecksTimeout` | `waitForNotificationDrain` 891: `if ctx.Err() != nil {` before `Wait()` | `ShutdownCompletes` in drain-timeout.cfg |
| `HandlerCtxCapsWait` | `processNotifications` 509-510 passes `handlerCtx`, and `waitNotificationsUpTo` 811-812 sets `target = min(target, h.seq-1)` before the loop | `ResponseReturnsCaller` in nested-handler.cfg: a handler's request on the same connection never returns, and the client handles no later notification |

`check.sh` also refuses to run (DRIFT, exit 2) when the code no longer has the shape that the model assumes: one `receive` goroutine, one `processNotifications` goroutine, handlers that get `c.inboundCtx` or a `handlerCtx` made from it, handlers and responses handled inline, and a non-blocking queue send. A new form of the cap in `waitNotificationsUpTo` is DRIFT too.

## Bounds and assumptions

- barrier.cfg: 3 notifications, queue capacity 2, one app caller, ctx cancel and EOF allowed, 48,455 states.
- slow-handler.cfg: 2 notifications, plain and slow handlers, no drain timeout, 36,602 states.
- drain-timeout.cfg: as slow-handler, and the timeout fires in the end, 83,448 states.
- nested-handler.cfg: 2 notifications, one of them with a handler that calls a request with its ctx. The peer answers that request in the end, or closes (`NestedSpec`). 155,948 states.
- deep-two-callers.cfg (`--deep`): 2 notifications, two app callers, 1,127,364 states.
- The real queue holds 1024 notifications. The model uses 2, and overflow takes the same code path.
- Time is not modeled. "5 s" means "in the end", and fairness gives each goroutine its turn.
- Mutexes, channels and `sync.Cond` act as the Go documentation says. `Wait` has no spurious wakeups.
- A request from a handler uses that handler's ctx. The model has one connection, and it does not model a ctx that another connection's handler passes in, a ctx that outlives its handler, or a request from a handler with an unrelated ctx. `TestSendRequest_WithOtherHandlerCtxWaitsForPreResponseNotification` covers the first two. Both keep the full wait. The `SendRequest` doc comment says that the third waits for the handler that made it.

Not covered: JSON parsing, request ID canonicalization, `$/cancel_request` (`handleCancelRequest`, `sendCancelRequests`), write errors from `sendMessage`, the inbound request goroutines (they appear only as callers), and the generated handlers in `agent_gen.go` and `client_gen.go`, such as the `sessionCancels` map. A pass covers the model within these bounds, not the whole SDK.

## Files

- `NotificationBarrier.tla`: the model and the properties.
- `barrier.cfg`, `slow-handler.cfg`, `drain-timeout.cfg`, `nested-handler.cfg`: the configs that `check.sh` runs. `deep-two-callers.cfg` runs with `--deep`.
- `chart.cfg`: a small config for a `/statechart` chart. `check.sh` does not run it. Append `facts.cfg` from a `check.sh` output folder to it before you use it.
- `statechart-view.mjs`: labels for `/statechart`.
- `check.sh`: reads the facts, runs TLC, compares `fingerprint.txt`.
- `fingerprint.txt`: sha256 of each covered function at the sync commit.

## Coverage record

- File: `connection.go`.
- Functions: `NewConnection`, `receive`, `shutdownReceive`, `processNotifications`, `handleResponse`, `SendRequest`, `SendRequestNoResult`, `waitForResponse`, `waitNotificationsUpTo`, `waitForNotificationDrain`.
- Sync commit: the last commit that changed `fingerprint.txt`, which `git log -1 --format=%H -- .agents/skills/verify-notification-barrier/fingerprint.txt` prints. The model was last confirmed to describe these functions at that commit, and the line numbers in this file are at that commit. A squash merge keeps the rule true, because the merged commit changes `fingerprint.txt` too.
- To see what changed since then: `git diff "$(git log -1 --format=%H -- .agents/skills/verify-notification-barrier/fingerprint.txt)" -- connection.go`. `check.sh` exit 3 names the changed functions.
