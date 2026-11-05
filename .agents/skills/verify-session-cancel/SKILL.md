---
name: verify-session-cancel
description: Model-check session/cancel handling in AgentSideConnection with TLA+ (TLC). A session/cancel must cancel the ctx of every prompt of its session that the client sent before it, whichever handler runs first, and must not cancel a prompt that the client sent after it. Use before you finish any change to cancelPrompt, registerPrompt or pruneCancelMarkers in agent.go, to the session/prompt or session/cancel case in agent_gen.go (or the emitter in cmd/generate/internal/emit/dispatch.go that writes them), to requestWatermark, oldestInflightWatermark, receive, handleInbound, waitInboundBarrier or the peer-wait count in SendRequest and SendRequestNoResult in connection.go, and when a bug report says that a cancel did not stop a turn or that a new prompt was canceled at once.
---

# Verify session/cancel

`session/cancel` is a notification, so it runs on the `processNotifications` goroutine. Each `session/prompt` runs on its own request goroutine. Either one can reach `a.mu` first. `cancelPrompt` cancels the prompt that has registered, and records a marker for a prompt that arrived before the cancel but registers later. `pruneCancelMarkers` drops a marker once no request that arrived before it is in flight. Unit tests reach a few of these orders (`agent_session_cancel_test.go`). This skill checks every order of a small model of that code with TLC.

The model is a hand translation. `check.sh` reads four facts from the code on every run and sets the model from them, so a change to one of those lines changes what TLC checks. It refuses to run (DRIFT) when a line that the model depends on has a new form. Any other change inside covered code makes it report STALE until you review the model against the code.

## What it proves

| Property | Promise | Harm if broken | Configs |
| --- | --- | --- | --- |
| `CancelReachesEarlierPrompts` | Once a `session/cancel` handler has run, every prompt of that session that the client sent before the cancel and that is still in `a.agent.Prompt` has a canceled ctx. This holds whether the prompt registered before or after the cancel ran. | The user stops a turn, and the agent keeps working. | one-prompt-at-a-time, two-sessions, peer-wait-earlier-prompt, overlapping-prompts |
| `CancelSparesLaterPrompts` | A `session/cancel` never cancels the ctx of a prompt that the client sent after it. It counts the cancels that `cancelPrompt` and the marker make, not the cancel of a replaced prompt in `registerPrompt`. | The next prompt ends at once as canceled. | one-prompt-at-a-time, two-sessions, peer-wait-later-prompt, cancel-waits-later-prompt |
| `TypeOK` and TLC's deadlock check | The state stays well formed, and the model ends only when every message is handled. TLC stops at the first broken rule, so in the finding configs the deadlock check does not finish. | | all |

"Sent before" uses the SDK's own clock: a prompt's watermark is the number of notifications that `receive` read before it, and a cancel's seq is its place among the notifications.

## Findings at the sync commit

The current code breaks both promises. `check.sh` expects these three results, and `replay.sh` reproduces each one against the real code.

1. **Overlapping prompts lose the cancel** (`overlapping-prompts.cfg`, `CancelReachesEarlierPrompts`, issue #83 and #72 item 4). The client sends prompt 1 and then prompt 2 for one session. Prompt 2 registers, and `registerPrompt` cancels prompt 1. Prompt 1 returns, and the `session/prompt` case runs `delete(a.sessionCancels, sessionId)`. That entry now holds prompt 2's cancel func. The client then sends `session/cancel`. `cancelPrompt` finds no entry, and prompt 2 runs on. Replay: `TestReplay_OverlappingPromptsKeepCancel`.
2. **The peer-wait release lets a cancel hit a later prompt** (`peer-wait-later-prompt.cfg`, `CancelSparesLaterPrompts`, issue #72 item 1). A notification handler calls the client with its ctx and waits for the answer. The client sends `session/cancel` and then a new `session/prompt`. `waitInboundBarrier` releases the prompt because the running handler waits on the peer (`waitNotifications(ctx, watermark, true)`), and the prompt registers. The handler then returns, the cancel runs, and `cancelPrompt` cancels the new prompt. Replay: `TestReplay_PeerWaitReleaseSparesLaterPrompt`.
3. **The same release, when the agent's `Cancel` calls the client** (`cancel-waits-later-prompt.cfg`, `CancelSparesLaterPrompts`, a second trigger of #72 item 1). The `session/cancel` case runs `a.agent.Cancel(ctx, p)` with the handler's ctx. When `Cancel` calls the client with it, that handler waits on the peer too. A second `session/cancel` and a new prompt that the client sends meanwhile take the path of finding 2. Replay: `TestReplay_CancelHandlerWaitSparesLaterPrompt`.

Each replay failed 10 of 10 runs at the sync commit, and all three passed on a scratch fix of both findings.

## When to run it

Run it when `git diff` touches covered code (see the coverage record at the end), or when your change writes `a.sessionCancels`, `a.sessionCancelledAt`, `c.inflightWatermarks`, the `handlingRequest` or `handlingNotification` ctx values, or `c.peerWaits`. Run it once before your change, so that you know the starting result, and once after it. A change to the barrier itself also needs `verify-notification-barrier`.

## Run

From the repository root:

```bash
.agents/skills/verify-session-cancel/check.sh --out "$OUT_DIR"
.agents/skills/verify-session-cancel/replay.sh
```

- `check.sh` needs Java 11 or newer, `curl`, `sha256sum` and `awk`. It downloads `tla2tools.jar` 1.7.4 into the same cache as `verify-notification-barrier` (`${ACP_TLA_CACHE:-${XDG_CACHE_HOME:-~/.cache}/acp-go-sdk-tla}`) and checks its sha256. Set `TLA2TOOLS_JAR` to use a copy that you have, and `JAVA` to pick a JVM. All six configs take about 10 seconds.
- `--out` keeps each config's TLC output, the effective config (`<config>.effective.cfg`) and `facts.cfg`. Without it, the script makes a temporary folder and prints its path.
- `replay.sh` needs Go. It copies `replay_test.go` into the package root as `zz_session_cancel_replay_test.go`, runs it, and removes the copy. It prints one line for each replay and a count line. It exits 1 while a replay breaks its promise, which is the expected result while the findings are open. It exits 2 when a replay could not follow its path (a `SETUP:` failure, such as a prompt that never starts), which says nothing about the promise.

## Read the result

| `check.sh` exit | Meaning | Next step |
| --- | --- | --- |
| 0 | Every config gave its expected result, and the covered code matches `fingerprint.txt`. | Done. |
| 1 | A config gave another result. The table names the result and the TLC output. | A property broke where it held: follow "Bug or model error". A finding config passed: follow "A finding is gone". |
| 2 | Setup error, or DRIFT: a line that the model depends on is gone or has a new form. | Install the missing tool, or update the model and `check.sh` for the new code (see "Update the verification"). A fix for a finding lands here first. |
| 3 | Every result was as expected, but covered code changed since the sync commit. This is not a pass. | Follow "Update the verification" before you trust the result. |

Read every TLC result with the `/statechart` skill. It is hidden from the default skill list, so load it by name. Give it `SessionCancel.tla`, the `<config>.effective.cfg` from the output folder and the view module `statechart-view.mjs` in this folder. Work in a scratch folder outside the repository. For a broken rule, its report tells the shortest path in plain words. For a pass, use `chart.cfg` (one prompt, then one cancel) with `facts.cfg` appended: the chart shows both handler orders and the marker. If `/statechart` rejects the view module because its interface changed, rewrite the module from `/statechart`'s instructions.

## Bug or model error

1. Tell the counterexample as steps with `/statechart`.
2. For each step, open the code in the mapping table, and check that the step can happen there in that order.
3. If one step cannot happen, the model is wrong. Fix the model, and say what was wrong.
4. If every step can happen, add a replay of the path to `replay_test.go` and run `replay.sh`. A replay that breaks its promise is a bug in the code. Report it with the steps and the harm. Do not change the property or the model to make it pass.

## A finding is gone

A fix for a finding usually changes a shape that `check.sh` checks, so it gives DRIFT first. A change to a fact can also make a finding config pass: `InboundStopsAtPeerWait = FALSE` makes peer-wait-later-prompt pass. Then:

1. Update the model action that the fix changed (see "Update the verification").
2. Run `check.sh`. The finding's config must now pass. Change its expected result in `CONFIGS` from the invariant name to `pass`. If another config now checks only part of the properties because of the finding, add the other property back.
3. Run `replay.sh`. The finding's replay must hold. Keep the replay, and move it into the package's tests if the fix PR wants a regression test.
4. Move the finding out of "Findings at the sync commit".

## Update the verification

Do this after every change to covered code, on purpose or not, and always after exit 2 (DRIFT) or 3:

1. Find each model action whose code changed, with the mapping table below. Edit the model from the code, not from the result. Keep one indivisible step of the code as one action: one critical section under `a.mu`, `c.mu` or `notifyMu`, or one channel operation.
2. If you change a fact or a shape that `check.sh` reads, update its extraction and the tables below.
3. Change a property only when the promise changed on purpose, and cite the doc comment, test or commit that says so.
4. Rerun `check.sh`. Then break the new code the way a property guards against, such as `cancelledAt >= watermark` in `registerPrompt`, and confirm that `check.sh` exits 1. Drop the break.
5. Run `check.sh --update-fingerprint` only after you confirm that the model describes the code. Commit `fingerprint.txt` in the same commit as the model change. That commit becomes the sync commit.

## Model to code

Lines are at the sync commit.

| Action | Code |
| --- | --- |
| `ReceivePrompt` | `receive` 456: `watermark := c.lastEnqueuedNotificationSeq` under `notifyMu`, and 465 puts it in the request ctx as `handlingRequest` |
| `ReceiveAddsInflight` | `receive` 467-470: `c.inflight[idKey]` and `c.inflightWatermarks[watermark]++` under `c.mu`, before `go func` |
| `ReceiveNotification` | `receive` 506-511: `c.lastEnqueuedNotificationSeq++` and the queue send under `notifyMu` |
| `RunCancel` | `processNotifications` 575-576 runs the `session/cancel` case (`agent_gen.go` 291-303), which calls `cancelPrompt` (`agent.go` 45-56) and then `a.agent.Cancel(ctx, p)` at 300. For a `cancelpeer` message, `Cancel` calls the client with that ctx, as in `StartPeerNote`. |
| `StartPeerNote`, `ClientAnswers` | a handler that calls `CallExtension` (or any request) with its ctx, through `handleWithExtensions` (`extensions.go` 37-51): `SendRequest` 744 or `SendRequestNoResult` 1068 calls `startPeerWait`, which counts `c.peerWaits[seq]` at 978, and its returned func 986 ends the count |
| `CompleteNotification` | `processNotifications` 584: `c.completedNotificationSeq = queued.seq` |
| `PassBarrier` | `waitInboundBarrier` 955: `waitNotifications(ctx, watermark, true)`, whose loop at 917 also ends while `c.peerWaits[c.completedNotificationSeq+1] > 0` |
| `Register` | the `session/prompt` case 406-409 calls `registerPrompt` (`agent.go` 66-79): `prev()` at 70, store at 72, marker check at 74, prune at 78 |
| `PromptReturns` | `a.agent.Prompt(reqCtx, p)` returns (`agent_gen.go` 410) |
| `Cleanup` | `agent_gen.go` 411-413: `delete(a.sessionCancels, string(p.SessionId))` under `a.mu`, whichever prompt the entry holds |
| `Respond` | `handleInbound` 662 runs the handler, and 685 sends the response with `c.sendMessage(res)` |
| `Exit` | `receive` 474-480: the goroutine's defer removes the request from `c.inflight` and `c.inflightWatermarks` |
| `Prune` | `pruneCancelMarkers` (`agent.go` 85-103) with `oldestInflightWatermark` (`connection.go` 994-1003) |

`agent_gen.go` comes from `cmd/generate/internal/emit/dispatch.go` (the `cancelPrompt` call at 42, `registerPrompt` at 66, the cleanup `delete` at 74). Change the emitter and regenerate. Do not edit `agent_gen.go` by hand.

## Facts that check.sh reads

| Fact | Code at the sync commit | What breaks when it changes |
| --- | --- | --- |
| `MarkerCancelsWhen` | `registerPrompt` 74: `ok && cancelledAt > watermark {`. `>=` gives `ge`, and no read of `a.sessionCancelledAt` gives `never`. | `ge`: `CancelSparesLaterPrompts` in one-prompt-at-a-time and two-sessions. `never`: `CancelReachesEarlierPrompts` in one-prompt-at-a-time, two-sessions and peer-wait-earlier-prompt. |
| `CancelRecordsMarker` | `cancelPrompt` 52-53: `a.sessionCancelledAt[sessionID] = seq` | `CancelReachesEarlierPrompts`: a prompt that registers after the cancel runs on |
| `PruneDropsWhen` | `pruneCancelMarkers` 88: `if !ok \|\| oldest >= cancelledAt {`. `>` gives `gt`, and no guard gives `always`. | `always`: `CancelReachesEarlierPrompts`. `gt` keeps markers longer and breaks nothing. |
| `InboundStopsAtPeerWait` | `waitInboundBarrier` 955 passes `true`, the loop at 917 checks `c.peerWaits`, `startPeerWait` 978 counts, and both `SendRequest` 744 and `SendRequestNoResult` 1068 call `startPeerWait(ctx)` before `waitForResponse`. Only one of the two gives DRIFT. | `FALSE`: peer-wait-later-prompt and cancel-waits-later-prompt pass, so `check.sh` reports that the findings are gone |

`check.sh` also gives DRIFT when one of these shapes changes: `cancelPrompt` cancels and deletes `a.sessionCancels[sessionID]`, `registerPrompt` calls `prev()` and stores `cancel`, both prune under `a.mu`, the `session/cancel` case calls `cancelPrompt` before `Cancel`, the `session/prompt` case registers, calls `Prompt`, then deletes the entry with no check of whose it is, `receive` records the watermark and the in-flight request before `go func`, `processNotifications` runs each handler with its seq and then completes it, `NewConnection` starts one `receive` and one `processNotifications`, `NewAgentSideConnection` dispatches through `handleWithExtensions`, which passes the handler ctx on, and `handleInbound` runs the handler with its ctx and then sends the response.

## Bounds and assumptions

- one-prompt-at-a-time.cfg: one session, a client that sends a prompt only after the previous prompt's response, cancels before, between and after two prompts, and an agent whose `Cancel` does not call the client, 257 states.
- two-sessions.cfg: two sessions with two prompts each, one at a time per session, cancels interleaved, and no handler that calls the client, 16,036 states.
- peer-wait-earlier-prompt.cfg: two notification handlers and two `Cancel` calls that wait on the client, with prompts sent before the cancels, 21,209 states. It checks only `CancelReachesEarlierPrompts`, because findings 2 and 3 break the other property here too.
- overlapping-prompts.cfg: two prompts of one session in flight at once, then a cancel. It checks only `CancelReachesEarlierPrompts`.
- peer-wait-later-prompt.cfg: a handler that waits on the client, a cancel, then a prompt. It checks only `CancelSparesLaterPrompts`.
- cancel-waits-later-prompt.cfg: a cancel whose `Cancel` waits on the client, a second cancel, then a prompt. It checks only `CancelSparesLaterPrompts`.
- A handler calls the client only where the script says so (`peernote`, `cancelpeer`). With such a handler in one-prompt-at-a-time or two-sessions, finding 2 breaks `CancelSparesLaterPrompts` there too.
- The client sends each script in order. The agent can return from `Prompt` at any step, canceled or not.
- Only prompts are requests in the model. Other requests in flight only keep markers longer.
- The model has fewer than `maxCancelMarkers` (1024) markers, so the eviction of the oldest marker never runs. A lost marker there loses only that peer's own cancel, as the comment on `maxCancelMarkers` says. `TestAgentSideConnection_SessionCancelMarkersArePruned` covers the cap.

Not covered: `$/cancel_request`, disconnect and the handler that starts with a canceled ctx after it, what `a.agent.Cancel` does other than call the client, which prompt wins when two prompts of one session register out of order (`registerPrompt` then cancels the prompt that the client sent later, and no doc says which one must win), the request ID that `receive` puts in `c.inflight`, a `session/cancel` sent as a request (it has no seq, so it records no marker), and the notification barrier, which `verify-notification-barrier` checks. A pass covers the model within these bounds, not the whole SDK.

## Files

- `SessionCancel.tla`: the model, the properties and the client scripts.
- `one-prompt-at-a-time.cfg`, `two-sessions.cfg`, `peer-wait-earlier-prompt.cfg`, `overlapping-prompts.cfg`, `peer-wait-later-prompt.cfg`, `cancel-waits-later-prompt.cfg`: the configs that `check.sh` runs.
- `chart.cfg`: a small config for a `/statechart` chart. `check.sh` does not run it.
- `statechart-view.mjs`: labels for `/statechart`.
- `check.sh`: reads the facts, runs TLC, compares `fingerprint.txt`.
- `replay_test.go`, `replay.sh`: the replays of the findings against the real code.
- `fingerprint.txt`: sha256 of each covered block at the sync commit.

## Coverage record

- `agent.go`: `NewAgentSideConnection`, `cancelPrompt`, `registerPrompt`, `pruneCancelMarkers`.
- `agent_gen.go`: the `AgentMethodSessionCancel` and `AgentMethodSessionPrompt` cases (generated by `cmd/generate/internal/emit/dispatch.go`).
- `extensions.go`: `AgentSideConnection.handleWithExtensions`.
- `connection.go`: `NewConnection`, `notificationSeq`, `requestWatermark`, `receive`, `processNotifications`, `handleInbound`, `SendRequest`, `SendRequestNoResult`, `waitNotifications`, `waitInboundBarrier`, `startPeerWait`, `oldestInflightWatermark`.
- Sync commit: the last commit that changed `fingerprint.txt`, which `git log -1 --format=%H -- .agents/skills/verify-session-cancel/fingerprint.txt` prints. The model was last confirmed to describe the covered code at that commit, and the line numbers in this file are at that commit.
- To see what changed since then: `git diff "$(git log -1 --format=%H -- .agents/skills/verify-session-cancel/fingerprint.txt)" -- agent.go agent_gen.go extensions.go connection.go`. `check.sh` exit 3 names the changed blocks.
