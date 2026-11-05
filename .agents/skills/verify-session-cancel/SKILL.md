---
name: verify-session-cancel
description: Model-check session/cancel handling in AgentSideConnection with TLA+ (TLC). A session/cancel must cancel the ctx of every prompt of its session that the client sent before it, whichever handler runs first, and must not cancel a prompt that the client sent after it. Of two overlapping prompts of one session, the one sent later must win. Use before you finish any change to cancelPrompt, registerPrompt or pruneCancelMarkers in agent.go, to the session/prompt or session/cancel case in agent_gen.go (or the emitter in cmd/generate/internal/emit/dispatch.go that writes them), to requestWatermark, requestArrival, oldestInflightWatermark, receive, handleInbound, waitInboundBarrier or the peer-wait count in SendRequest and SendRequestNoResult in connection.go, and when a bug report says that a cancel did not stop a turn or that a new prompt was canceled at once.
---

# Verify session/cancel

`session/cancel` is a notification, so it runs on the `processNotifications` goroutine. Each `session/prompt` runs on its own request goroutine. Either one can reach `a.mu` first. `cancelPrompt` cancels the prompt that has registered, and records a marker for a prompt that arrived before the cancel but registers later. `pruneCancelMarkers` drops a marker once no request that arrived before it is in flight. Unit tests reach a few of these orders (`agent_session_cancel_test.go`). This skill checks every order of a small model of that code with TLC.

The model is a hand translation. `check.sh` reads four facts from the code on every run and sets the model from them, so a change to one of those lines changes what TLC checks. It refuses to run (DRIFT) when a line that the model depends on has a new form. Any other change inside covered code makes it report STALE until you review the model against the code.

## What it proves

| Property | Promise | Harm if broken | Configs |
| --- | --- | --- | --- |
| `CancelReachesEarlierPrompts` | Once a `session/cancel` handler has run, every prompt of that session that the client sent before the cancel and that is still in `a.agent.Prompt` has a canceled ctx. This holds whether the prompt registered before or after the cancel ran. | The user stops a turn, and the agent keeps working. | all |
| `CancelSparesLaterPrompts` | A `session/cancel` never cancels the ctx of a prompt that the client sent after it. It counts the cancels that `cancelPrompt` and the marker make, not the cancel of a replaced prompt in `registerPrompt`. | The next prompt ends at once as canceled. | all |
| `NewerPromptSurvives` | When two prompts of one session overlap, `registerPrompt` cancels the one that the client sent first and keeps the later one, whichever registers first. | The user's newest prompt ends at once as canceled, and the stale turn keeps working. | all |
| `TypeOK` and TLC's deadlock check | The state stays well formed, and the model ends only when every message is handled. TLC stops at the first broken rule, so after a break the deadlock check does not finish. | | all |

"Sent before" uses the SDK's own clock: a prompt's watermark is the number of notifications that `receive` read before it, and a cancel's seq is its place among the notifications.

## Findings at the sync commit

None. Every config passes. The model found four bugs, and each one now has a regression test in `agent_session_cancel_test.go` and a fact that `check.sh` reads:

| Bug | Config | Test | Fact |
| --- | --- | --- | --- |
| Overlapping prompts lose the cancel (#83). The cleanup of an older prompt deleted a newer prompt's entry, so a later cancel found nothing. | overlapping-prompts | `TestAgentSideConnection_OverlappingPromptsKeepCancel` | `CleanupChecksOwner` |
| A notification handler waits on the client, and the barrier starts a prompt sent after a queued cancel. The cancel then cancels it (#72 item 1). | peer-wait-later-prompt | `TestAgentSideConnection_SessionCancelSparesPromptReleasedByPeerWait` | `CancelSkipsLaterPrompt` |
| The same release, when the agent's `Cancel` calls the client. | cancel-waits-later-prompt | `TestAgentSideConnection_SessionCancelSparesPromptReleasedByCancelWait` | `CancelSkipsLaterPrompt` |
| Two prompts of one session sent back to back: the newer handler often registered first, and the older one then canceled it and ran on (284 of 300 runs before the fix). | overlapping-prompts | `TestAgentSideConnection_NewerPromptWinsWhicheverRegistersFirst` | `RegisterKeepsNewer` |

When `check.sh` finds a new break, record it here with its config, its replay and the harm until a fix lands.

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
- `replay.sh` needs Go. It copies `replay_test.go` into the package root as `zz_session_cancel_replay_test.go`, runs its `TestReplay_` tests, and removes the copy. It prints one line for each replay and a count line. It exits 1 while a replay breaks its promise, and 2 when a replay could not follow its path (a `SETUP:` failure, such as a prompt that never starts), which says nothing about the promise. With no open finding, it runs no replay and prints `0 replays`.

## Read the result

| `check.sh` exit | Meaning | Next step |
| --- | --- | --- |
| 0 | Every config gave its expected result, and the covered code matches `fingerprint.txt`. | Done. |
| 1 | A config gave another result. The table names the result and the TLC output. | A property broke where it held: follow "Bug or model error". A config that expects an open finding passed: follow "A finding is gone". |
| 2 | Setup error, or DRIFT: a line that the model depends on is gone or has a new form. | Install the missing tool, or update the model and `check.sh` for the new code (see "Update the verification"). A fix for a finding lands here first. |
| 3 | Every result was as expected, but covered code changed since the sync commit. This is not a pass. | Follow "Update the verification" before you trust the result. |

Read every TLC result with the `/statechart` skill. It is hidden from the default skill list, so load it by name. Give it `SessionCancel.tla`, the `<config>.effective.cfg` from the output folder and the view module `statechart-view.mjs` in this folder. Work in a scratch folder outside the repository. For a broken rule, its report tells the shortest path in plain words. For a pass, use `chart.cfg` (one prompt, then one cancel) with `facts.cfg` appended: the chart shows both handler orders and the marker. If `/statechart` rejects the view module because its interface changed, rewrite the module from `/statechart`'s instructions.

## Bug or model error

1. Tell the counterexample as steps with `/statechart`.
2. For each step, open the code in the mapping table, and check that the step can happen there in that order.
3. If one step cannot happen, the model is wrong. Fix the model, and say what was wrong.
4. If every step can happen, add a replay of the path to `replay_test.go` and run `replay.sh`. A replay that breaks its promise is a bug in the code. Report it with the steps and the harm. Do not change the property or the model to make it pass. Until it is fixed, set its config's expected result in `CONFIGS` to the broken invariant, and record it under "Findings at the sync commit".

## A finding is gone

A fix for a finding usually changes a shape that `check.sh` checks, so it gives DRIFT first. Then:

1. Update the model action that the fix changed (see "Update the verification"). Add the fix as a fact that `check.sh` reads, as `CancelSkipsLaterPrompt` and `CleanupChecksOwner` do, so that a revert of the fix makes `check.sh` fail.
2. Run `check.sh`. The finding's config must now pass. Change its expected result in `CONFIGS` from the invariant name to `pass`. If a config checks only part of the properties because of the finding, add the other property back.
3. Run `replay.sh`. The finding's replay must hold. Move it into `agent_session_cancel_test.go` as a regression test, with a `TestAgentSideConnection_` name.
4. Move the finding into the table under "Findings at the sync commit".

## Update the verification

Do this after every change to covered code, on purpose or not, and always after exit 2 (DRIFT) or 3:

1. Find each model action whose code changed, with the mapping table below. Edit the model from the code, not from the result. Keep one indivisible step of the code as one action: one critical section under `a.mu`, `c.mu` or `notifyMu`, or one channel operation.
2. If you change a fact or a shape that `check.sh` reads, update its extraction and the tables below.
3. Change a property only when the promise changed on purpose, and cite the doc comment, test or commit that says so.
4. Rerun `check.sh`. Then break the new code the way a property guards against, such as `cancelledAt >= watermark` in `registerPrompt`, and confirm that `check.sh` exits 1. Drop the break.
5. Run `check.sh --update-fingerprint` only after you confirm that the model describes the code. Commit `fingerprint.txt` in the same commit as the model change. That commit becomes the sync commit.

## Model to code

The code column cites functions and the line that each step depends on. `check.sh` checks these lines (see the next section), and the coverage record pins them.

| Action | Code |
| --- | --- |
| `ReceivePrompt` | `receive`: `watermark := c.lastEnqueuedNotificationSeq` under `notifyMu`, then `c.inboundRequests++`, both put into the request ctx as `handlingRequest{conn: c, watermark: watermark, arrival: c.inboundRequests}`. The model takes a prompt's index in the script as its arrival. |
| `ReceiveAddsInflight` | `receive`: `c.inflight[idKey] = cancel` and `c.inflightWatermarks[watermark]++` under `c.mu`, before `go func` |
| `ReceiveNotification` | `receive`: `c.lastEnqueuedNotificationSeq++` and the send to `c.notificationQueue` under `notifyMu` |
| `RunCancel` | `processNotifications` runs the `AgentMethodSessionCancel` case, which calls `cancelPrompt` and then `a.agent.Cancel(ctx, p)`. `cancelPrompt` cancels and deletes the entry unless `pc.watermark >= seq`, records `a.sessionCancelledAt[sessionID] = seq`, and prunes. For a `cancelpeer` message, `Cancel` calls the client with that ctx, as in `StartPeerNote`. |
| `StartPeerNote`, `ClientAnswers` | a handler that calls `CallExtension` (or any request) with its ctx, through `handleWithExtensions`: `SendRequest` or `SendRequestNoResult` calls `startPeerWait`, which counts `c.peerWaits[seq]++`, and its returned func ends the count |
| `CompleteNotification` | `processNotifications`: `c.completedNotificationSeq = queued.seq` |
| `PassBarrier` | `waitInboundBarrier`: `c.waitNotifications(ctx, watermark, true)`, whose loop also ends while `c.peerWaits[c.completedNotificationSeq+1] > 0` |
| `Register` | the `AgentMethodSessionPrompt` case calls `registerPrompt`: `pc := &promptCancel{...}` with the prompt's `requestWatermark` and `requestArrival`. When the registered prompt arrived later (`prev.arrival > arrival`), it calls `cancel()` and returns. Otherwise `prev.cancel()`, `a.sessionCancels[sessionID] = pc`, the marker check `cancelledAt > watermark`, and the prune. |
| `PromptReturns` | `a.agent.Prompt(reqCtx, p)` returns |
| `Cleanup` | `donePrompt()`, the func that `registerPrompt` returned: under `a.mu`, `if a.sessionCancels[sessionID] == pc` then `delete` |
| `Respond` | `handleInbound`: `c.handler(ctx, req.Method, req.Params)`, then `c.sendMessage(res)` |
| `Exit` | `receive`: the request goroutine's defer removes the request from `c.inflight` and `c.inflightWatermarks` |
| `Prune` | `pruneCancelMarkers` with `oldestInflightWatermark` |

`agent_gen.go` comes from `WriteDispatchJen` in `cmd/generate/internal/emit/dispatch.go`, which writes the `cancelPrompt` call, the `donePrompt := a.registerPrompt(...)` line and the `donePrompt()` call. Change the emitter and regenerate. Do not edit `agent_gen.go` by hand.

## Facts that check.sh reads

| Fact | Code at the sync commit | What breaks when it changes |
| --- | --- | --- |
| `MarkerCancelsWhen` | `registerPrompt`: `if hasWatermark && cancelledAt > watermark {`. `>=` gives `ge`, and no read of `a.sessionCancelledAt` gives `never`. | `ge`: `CancelSparesLaterPrompts` in one-prompt-at-a-time and two-sessions. `never`: `CancelReachesEarlierPrompts`. |
| `CancelRecordsMarker` | `cancelPrompt`: `if hasSeq {` then `a.sessionCancelledAt[sessionID] = seq` | `CancelReachesEarlierPrompts`: a prompt that registers after the cancel runs on |
| `PruneDropsWhen` | `pruneCancelMarkers`: `if !ok \|\| oldest >= cancelledAt {`. `>` gives `gt`, and no guard gives `always`. | `always`: `CancelReachesEarlierPrompts`. `gt` keeps markers longer and breaks nothing. |
| `CancelSkipsLaterPrompt` | `cancelPrompt`: `if pc, ok := a.sessionCancels[sessionID]; ok && !(hasSeq && pc.hasWatermark && pc.watermark >= seq) {`. A plain `ok {` gives `FALSE`. | `FALSE`: `CancelSparesLaterPrompts` in peer-wait-earlier-prompt, peer-wait-later-prompt and cancel-waits-later-prompt (#72 item 1) |
| `RegisterKeepsNewer` | `registerPrompt`: `if hasWatermark && prev.hasWatermark && prev.arrival > arrival {`, then `cancel()` and `return func() {}`. No `prev.arrival` gives `FALSE`. | `FALSE`: `NewerPromptSurvives` in overlapping-prompts |
| `CleanupChecksOwner` | the func that `registerPrompt` returns: `if a.sessionCancels[sessionID] == pc {` right before `delete(a.sessionCancels, sessionID)`. A bare delete after `defer a.mu.Unlock()` gives `FALSE`. | `FALSE`: `CancelReachesEarlierPrompts` in overlapping-prompts (#83) |
| `InboundStopsAtPeerWait` | `waitInboundBarrier` passes `true`, the `waitNotifications` loop checks `c.peerWaits`, `startPeerWait` counts, and both `SendRequest` and `SendRequestNoResult` call `donePeerWait := c.startPeerWait(ctx)` before `waitForResponse`. Only one of the two gives DRIFT. | `FALSE` hides the release, so the peer-wait configs no longer reach the case that `CancelSkipsLaterPrompt` guards. Nothing fails, so read the facts on every run. |

`check.sh` also gives DRIFT when one of these shapes changes: `cancelPrompt` reads its seq, then cancels and deletes `a.sessionCancels[sessionID]`, `requestArrival` reads the arrival from the `handlingRequest` ctx value, `registerPrompt` reads the watermark and arrival, calls `prev.cancel()` and stores a `promptCancel`, both prune under `a.mu`, the func that `registerPrompt` returns deletes the entry under `a.mu`, the `session/cancel` case calls `cancelPrompt` before `Cancel`, the `session/prompt` case registers, calls `Prompt`, then calls `donePrompt()`, `receive` records the watermark, counts the request in `c.inboundRequests` and records the in-flight request before `go func`, `processNotifications` runs each handler with its seq and then completes it, `NewConnection` starts one `receive` and one `processNotifications`, `NewAgentSideConnection` dispatches through `handleWithExtensions`, which passes the handler ctx on, and `handleInbound` runs the handler with its ctx and then sends the response.

## Bounds and assumptions

- one-prompt-at-a-time.cfg: one session, a client that sends a prompt only after the previous prompt's response, cancels before, between and after two prompts, and an agent whose `Cancel` does not call the client, 257 states.
- two-sessions.cfg: two sessions with two prompts each, one at a time per session, cancels interleaved, and no handler that calls the client, 16,036 states.
- peer-wait-earlier-prompt.cfg: two notification handlers and two `Cancel` calls that wait on the client, with prompts sent before and after the cancels, 17,153 states.
- overlapping-prompts.cfg: three prompts of one session in flight at once, two cancels, and a notification handler that waits on the client, 11,460 states.
- peer-wait-later-prompt.cfg: a handler that waits on the client, a cancel, then a prompt, 53 states.
- cancel-waits-later-prompt.cfg: a cancel whose `Cancel` waits on the client, a second cancel, then a prompt, 53 states.
- A handler calls the client only where the script says so (`peernote`, `cancelpeer`).
- The client sends each script in order. The agent can return from `Prompt` at any step, canceled or not.
- Only prompts are requests in the model. Other requests in flight only keep markers longer.
- The model has fewer than `maxCancelMarkers` (1024) markers, so the eviction of the oldest marker never runs. A lost marker there loses only that peer's own cancel, as the comment on `maxCancelMarkers` says. `TestAgentSideConnection_SessionCancelMarkersArePruned` covers the cap.

Not covered, with what a user sees when it goes wrong:

- `$/cancel_request`: a canceled request still runs. `verify-notification-barrier` checks it (`CancelBeforeCheckSkipsHandler`).
- Disconnect: a prompt keeps running after the client exits. `verify-notification-barrier` checks that handlers get a canceled ctx.
- What `a.agent.Cancel` does other than call the client: the agent's own cancel logic is the agent's code.
- A `session/cancel` sent as a request: it has no seq, so it records no marker and cancels the registered prompt whenever it arrived.
- The request ID that `receive` puts in `c.inflight`.

A pass covers the model within these bounds, not the whole SDK.

## Files

- `SessionCancel.tla`: the model, the properties and the client scripts.
- `one-prompt-at-a-time.cfg`, `two-sessions.cfg`, `peer-wait-earlier-prompt.cfg`, `overlapping-prompts.cfg`, `peer-wait-later-prompt.cfg`, `cancel-waits-later-prompt.cfg`: the configs that `check.sh` runs.
- `chart.cfg`: a small config for a `/statechart` chart. `check.sh` does not run it.
- `statechart-view.mjs`: labels for `/statechart`.
- `check.sh`: reads the facts, runs TLC, compares `fingerprint.txt`.
- `replay_test.go`, `replay.sh`: replays of open findings against the real code. The helpers live in `agent_session_cancel_test.go`.
- `fingerprint.txt`: sha256 of each covered block at the sync commit.

## Coverage record

- `agent.go`: `NewAgentSideConnection`, `cancelPrompt`, `registerPrompt`, `pruneCancelMarkers`.
- `agent_gen.go`: the `AgentMethodSessionCancel` and `AgentMethodSessionPrompt` cases (generated by `cmd/generate/internal/emit/dispatch.go`).
- `extensions.go`: `AgentSideConnection.handleWithExtensions`.
- `connection.go`: `NewConnection`, `notificationSeq`, `requestWatermark`, `requestArrival`, `receive`, `processNotifications`, `handleInbound`, `SendRequest`, `SendRequestNoResult`, `waitNotifications`, `waitInboundBarrier`, `startPeerWait`, `oldestInflightWatermark`.
- Sync commit: the last commit that changed `fingerprint.txt`, which `git log -1 --format=%H -- .agents/skills/verify-session-cancel/fingerprint.txt` prints. The model was last confirmed to describe the covered code at that commit, and the line numbers in this file are at that commit.
- To see what changed since then: `git diff "$(git log -1 --format=%H -- .agents/skills/verify-session-cancel/fingerprint.txt)" -- agent.go agent_gen.go extensions.go connection.go`. `check.sh` exit 3 names the changed blocks.
