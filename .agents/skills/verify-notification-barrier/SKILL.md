---
name: verify-notification-barrier
description: Model-check the notification barrier in connection.go with TLA+ (TLC). The barrier makes SendRequest (and so Prompt and every other request) return only after the handlers of all notifications that arrived before its response have finished, and makes shutdown drain those handlers. Use before you finish any change to NewConnection, receive, shutdownReceive, processNotifications, handleResponse, SendRequest, SendRequestNoResult, waitForResponse, waitNotificationsUpTo or waitForNotificationDrain, and when a bug report says that Prompt returned before its session/update handlers finished, that a request hangs, or that a handler context ended too early.
---

# Verify the notification barrier

`connection.go` has one reader goroutine, one goroutine that runs notification handlers in order, and any number of goroutines that wait in `SendRequest`. Four fixes already went into this code (#5, #8, #10, #30), and unit tests reach few of its interleavings. For example, `go test ./...` still passes when `SendRequestNoResult` stops waiting for earlier handlers, and `check.sh` fails on it. This skill checks every interleaving of a small model of that code with TLC.

The model is a hand translation. `check.sh` reads nine facts from `connection.go` on every run and sets the model from them, so a change to one of those lines changes what TLC checks. Any other change inside a covered function makes `check.sh` report STALE until you review the model against the code.

## What it proves

Each property is one promise to SDK users. The config names show where TLC checks it.

| Property | Promise | Harm if broken | Configs |
| --- | --- | --- | --- |
| `BarrierHolds` | When `SendRequest` returns a response, the handler of every notification that the peer sent before that response has returned. | `Prompt()` returns while `session/update` handlers still run, so a client renders the end of a turn before its last chunks. | all |
| `InboundCtxOutlivesDrain` | After a disconnect, handlers of notifications that were already read keep a live ctx until they return, or until the 5 s drain timeout. | Handlers that respect ctx drop the last messages of a session. | all |
| `ResponseReturnsCaller` | Once the response is read, the caller returns. It needs no disconnect, no ctx cancel and no timeout. | A request hangs although its answer arrived. | barrier, deep, nested-handler |
| `CancelReturnsCaller` | A canceled ctx makes the caller return, also while a handler that it waits for never finishes. | Cancel does not stop a stuck request. | slow-handler |
| `DisconnectReturnsCaller` | A disconnect makes the caller return at once, also while a handler that it waits for never finishes. | Callers hang after the peer exits. | slow-handler |
| `ShutdownCompletes` | After a disconnect, handler contexts end in the end. | Goroutines and handler work leak after every session. | barrier, drain-timeout, deep |
| `NoDeadlock` (`CHECK_DEADLOCK`) | No state exists where every goroutine is stuck and nothing can move. | The connection freezes. | all |

## Known finding

`nested-handler.cfg` fails on the current code, and `check.sh` expects that failure.

- Rule: `ResponseReturnsCaller`.
- In one sentence: a notification handler that calls a request on the same connection never gets its result, and every later notification waits forever.
- Steps, in the code's words:
  1. The peer sends a `session/update`. `receive` gives it sequence 1 and queues it.
  2. `processNotifications` calls the client's `SessionUpdate` handler with `c.inboundCtx`.
  3. The handler calls `CallExtension` (or any other request) on the same connection.
  4. The peer answers. `handleResponse` records watermark 1, because `lastEnqueuedNotificationSeq` is 1.
  5. `waitNotificationsUpTo(ctx, 1)` waits until `completedNotificationSeq` reaches 1.
  6. Only `processNotifications` can raise `completedNotificationSeq`, and it is the goroutine that waits in step 5. Nothing wakes it until the peer disconnects. `c.inboundCtx` is canceled only after a disconnect, so it does not help.
- Harm: the client freezes. It handles no further `session/update` until the peer exits. An app goroutine whose response arrives after that update hangs too.
- Evidence on the real code: a scratch test with a client whose `SessionUpdate` handler calls `clientConn.CallExtension(ctx, "_verify/ping", ...)`. After 2 s the call had not returned, `completedNotificationSeq` was 0, and a second update stayed unhandled. The test was not committed. To rebuild it, use `newNotificationBarrierTestPair`, `clientFuncs` and `agentFuncs` from the package tests.

When a fix lands, `nested-handler.cfg` passes, and `check.sh` exits 1 with "should reproduce the known finding". Then change its expected result in `check.sh` from `13` to `0`, move this section into the property table, and move the sync commit.

## When to run it

Run it when `git diff` touches any covered function (see the coverage record at the end), or when the code that you changed writes `lastEnqueuedNotificationSeq`, `completedNotificationSeq`, `notificationQueue`, `pending`, `notifyCond`, `inboundCtx` or `c.ctx`. Run it once before your change, so that you know the starting result, and once after it.

## Run

From the repository root:

```bash
.agents/skills/verify-notification-barrier/check.sh --out "$OUT_DIR"
```

- It needs Java 11 or newer, `curl`, `sha256sum` and `awk`.
- The first run downloads `tla2tools.jar` 1.7.4 into `${ACP_TLA_CACHE:-${XDG_CACHE_HOME:-~/.cache}/acp-go-sdk-tla}` and checks its sha256 on every run. To use a copy that you already have, set `TLA2TOOLS_JAR`. To pick a JVM, set `JAVA`. The default heap is 4 GB: set `TLC_HEAP` to change it.
- It takes about 1 minute. `--deep` adds `deep-two-callers.cfg`: two app goroutines call at once, which takes about 15 more minutes (1,127,364 states at the sync commit).
- `--out` keeps each config's TLC output, the effective config (`<config>.effective.cfg`, the static config plus the facts) and `facts.cfg`. Without `--out`, the script makes a temporary folder and prints its path.

## Read the result

| Exit | Meaning | Next step |
| --- | --- | --- |
| 0 | Every config gave its expected result, and the covered functions match `fingerprint.txt`. | Done. |
| 1 | A property failed that holds at the sync commit, or the known finding stopped reproducing. | Read the TLC output named in the summary. Then follow "Bug or model error". |
| 2 | Setup error, or DRIFT: a line that the model depends on is gone or has a new form. | Install the missing tool, or update the model and `check.sh` for the new code shape. |
| 3 | Every result was as expected, but covered functions changed since the sync commit. | Review the mapping below against the new code. Then follow "Update the verification". |

Read every TLC result with the `/statechart` skill. Give it `NotificationBarrier.tla`, the `<config>.effective.cfg` from the output folder and the view module `statechart-view.mjs` in this folder. Work in a scratch folder outside the repository. For a failure, its report tells the shortest path to the broken rule in plain words. For a pass on `chart.cfg` (one update, one request), it draws how a request waits for an earlier handler. The other configs pass with too many states for a chart, and the report then lists the rules that hold. If `/statechart` rejects the view module because its interface changed, rewrite the module from `/statechart`'s instructions.

## Bug or model error

1. Tell the counterexample as steps with `/statechart`.
2. For each step, open the code in the mapping table, and check that the step can happen there in that order.
3. If one step cannot happen, the model is wrong. Fix the model, and say what was wrong.
4. If every step can happen, replay the path against the real code in a scratch `_test.go` file in the package, as in "Known finding". Then delete the file. A failing replay is a bug in the code. Report it with the steps and the harm. Do not change the property or the model to make it pass.

## Update the verification

When you change covered code on purpose:

1. Edit the model from the code, not from the result. Keep one indivisible step of the code as one action: one critical section under `notifyMu`, one channel operation, or one `select`.
2. If you change a fact that `check.sh` reads, update its extraction and the facts table below.
3. Change a property only when the promise changed on purpose, and cite the doc comment, test or commit that says so.
4. Rerun `check.sh`. Then break the new code the way the property guards against, such as removing a check or reordering a step, and confirm that `check.sh` exits 1. Drop the break.
5. Run `check.sh --update-fingerprint`, and set the sync commit below to the commit that you checked. Move the sync commit only after you confirm that the model describes the code at that commit.

## Model to code

Lines are at the sync commit.

| Model action | Code in `connection.go` |
| --- | --- |
| `PeerSendsNote`, `PeerResponds`, `PeerCloses` | The peer writes a notification, a response or EOF to the stream. They have no fairness: the peer can stop at any time. |
| `RecvNotification` | `receive`, 428-452: `lastEnqueuedNotificationSeq++` and the non-blocking send under `notifyMu`. On a full queue: rollback (442), then `shutdownReceive(errNotificationQueueOverflow)`. |
| `RecvResponse` | `handleResponse`, 518-539: take `c.pending[idStr]` under `c.mu`, read the watermark under `notifyMu`. |
| `RecvDeliver` | `handleResponse`, 540: `pr.ch <- responseEnvelope{...}`. |
| `RecvEOF` | `receive`, 458-462: `scanner.Scan()` ends. |
| `ShutdownCancel` | `shutdownReceive`, 471: `c.cancel(cause)`. |
| `ShutdownClose` | `shutdownReceive`, 476-491: close the queue, read `finalEnqueuedSeq`, start the drain goroutine. One action, because only `receive` writes `lastEnqueuedNotificationSeq`. |
| `WorkerDequeue`, `WorkerExit` | `processNotifications`, 499: `for queued := range c.notificationQueue`. |
| `PlainHandlerReturns`, `SlowHandlerReturns`, `NestedHandlerCalls`, `NestedHandlerReturns` | `processNotifications`, 500: `c.handleInbound(c.inboundCtx, queued.msg)` runs the user's handler. Plain handlers return promptly (fair). Slow handlers return after any time (no fairness). A nested handler calls `SendRequest` on this goroutine with `c.inboundCtx`. |
| `WorkerComplete` | `processNotifications`, 502-514: `completedNotificationSeq = queued.seq` and `Broadcast` under `notifyMu`. |
| `AppCalls` | `SendRequest` 631-639, `SendRequestNoResult` 882-890: register `c.pending[idKey]`. The app decides when, so no fairness. |
| `SendsRequest` | `SendRequest` 641, `SendRequestNoResult` 892: `c.sendMessage(msg)`. |
| `GetsResponse`, `SeesCtxDone`, `CtxBranchChecksDone`, `SeesDisconnect`, `CleansUp` | `waitForResponse`, 743-773. |
| `EntersBarrier` | `waitNotificationsUpTo`, 776-800, called at 650 and 901: return for target 0, else lock and start the ctx waker. |
| `BarrierLoop`, `BarrierChecksDone`, `BarrierChecksCtxOrWaits`, `BarrierRelocks` | `waitNotificationsUpTo`, 802-831. `notifyCond.Wait()` releases the lock and sleeps in one step. |
| `CtxWakerFires`, `CtxWakerStops` | `waitNotificationsUpTo`, 791-800: the goroutine that broadcasts on `ctx.Done()`. |
| `ConnWatcherFires` | `NewConnection`, 114-119: the goroutine that broadcasts on `c.ctx.Done()`. |
| `DrainStarts`, `DrainLoop`, `DrainChecksTimeoutOrWaits`, `DrainRelocks` | `waitForNotificationDrain`, 835-871. |
| `DrainWakerFires`, `DrainWakerStops` | `waitForNotificationDrain`, 852-861: the goroutine that broadcasts on the timeout. |
| `DrainTimerFires` | `context.WithTimeout`, 840, with `notificationQueueDrainTimeout` (5 s, line 18). |
| `InboundCancel` | `shutdownReceive`, 490: `c.inboundCancel(cause)`. |
| `AppCancelsCtx` | The caller's ctx ends. No fairness. |

## Facts that check.sh reads

Each fact sets one model constant. The last column is the result of setting the fact to its broken value at the sync commit, which shows that each property can fail.

| Fact | Code at the sync commit | Broken value makes this fail |
| --- | --- | --- |
| `WaitsForBarrier` | `SendRequest` 650 and `SendRequestNoResult` 901 call `waitNotificationsUpTo(ctx, resp.notificationWatermark)` | `BarrierHolds` in barrier.cfg (the bug of #5) |
| `WatermarkSource` | `handleResponse` 534: `watermark := c.lastEnqueuedNotificationSeq` | `BarrierHolds` in barrier.cfg (with `completedNotificationSeq`) |
| `CompletesAfterHandler` | `processNotifications`: `handleInbound` (500) before `completedNotificationSeq = queued.seq` (508) | `InboundCtxOutlivesDrain` in barrier.cfg |
| `WorkerBroadcasts` | `processNotifications` 513: `Broadcast` after the count | `ResponseReturnsCaller` in barrier.cfg |
| `RollsBackOnOverflow` | `receive` 442: `lastEnqueuedNotificationSeq--` | `NoDeadlock` in barrier.cfg: the drain waits for a notification that was never queued |
| `DrainBeforeInboundCancel` | `shutdownReceive` 489-490 | `InboundCtxOutlivesDrain` in barrier.cfg |
| `CtxWakerSpawned` | `waitNotificationsUpTo` 791-800 | `CancelReturnsCaller` in slow-handler.cfg |
| `ConnDoneWaker` | `NewConnection` 114-119 | `DisconnectReturnsCaller` in slow-handler.cfg |
| `DrainTimerWaker` | `waitForNotificationDrain` 852-861 | `ShutdownCompletes` in drain-timeout.cfg |

`check.sh` also refuses to run (DRIFT, exit 2) when the code no longer has the shape that the model assumes: one `receive` goroutine, one `processNotifications` goroutine, handlers and responses handled inline, and a non-blocking queue send.

## Bounds and assumptions

- barrier.cfg: 3 notifications, queue capacity 2, one app caller, ctx cancel and EOF allowed, 48,455 states.
- slow-handler.cfg: 2 notifications, plain and slow handlers, no drain timeout, 36,602 states.
- drain-timeout.cfg: as slow-handler, and the timeout fires in the end, 83,448 states.
- nested-handler.cfg: 2 notifications, one of them with a handler that calls a request.
- deep-two-callers.cfg (`--deep`): 2 notifications, two app callers, 1,127,364 states.
- The real queue holds 1024 notifications. The model uses 2, and overflow takes the same code path.
- Time is not modeled. "5 s" means "in the end", and fairness gives each goroutine its turn.
- Mutexes, channels and `sync.Cond` act as the Go documentation says. `Wait` has no spurious wakeups.

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
- Sync commit: `0da7315c23b5e9c1023f701a64b2bb4969b61042`. The model was last confirmed to describe these functions at this commit.
- To see what changed since then: `git diff 0da7315c23b5e9c1023f701a64b2bb4969b61042 -- connection.go`. `check.sh` exit 3 names the changed functions.
