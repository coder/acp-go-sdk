---------------------------- MODULE SessionCancel ----------------------------
(***************************************************************************)
(* session/cancel handling in AgentSideConnection (agent.go, agent_gen.go) *)
(* and the parts of connection.go that it relies on. SKILL.md maps each    *)
(* action to the code.                                                     *)
(*                                                                         *)
(* The client sends Script in order. Each prompt runs on its own request   *)
(* goroutine. Notifications (session/cancel and an extension notification  *)
(* whose handler waits on the client) run in order on processNotifications.*)
(***************************************************************************)
EXTENDS Naturals, Sequences, FiniteSets

CONSTANTS
    Script,
    \* Facts that check.sh reads from the code.
    MarkerCancelsWhen,      \* registerPrompt: "gt" (cancelledAt > watermark), "ge", or "never"
    CancelRecordsMarker,    \* cancelPrompt stores sessionCancelledAt[sessionID] = seq
    PruneDropsWhen,         \* pruneCancelMarkers: "ge" (oldest >= cancelledAt), "gt", or "always"
    CancelSkipsLaterPrompt, \* cancelPrompt leaves a registered prompt with watermark >= the cancel's seq
    CleanupChecksOwner,     \* the prompt cleanup deletes the session's entry only while it is its own
    InboundStopsAtPeerWait  \* waitInboundBarrier also ends while the running handler waits on the peer

ASSUME MarkerCancelsWhen \in {"gt", "ge", "never"}
ASSUME PruneDropsWhen \in {"ge", "gt", "always"}
ASSUME CancelRecordsMarker \in BOOLEAN /\ InboundStopsAtPeerWait \in BOOLEAN
ASSUME CancelSkipsLaterPrompt \in BOOLEAN /\ CleanupChecksOwner \in BOOLEAN

\* One message from the client. k is "prompt", "cancel", "cancelpeer" (a
\* session/cancel whose a.agent.Cancel calls the client with its ctx) or
\* "peernote" (an extension notification whose handler calls the client with
\* its ctx). s is the session. after > 0: the client sends it only once it has
\* the response to the prompt at that index.
Msg(k, s, after) == [k |-> k, s |-> s, after |-> after]

N == Len(Script)
Idx == 1..N
Prompts == {i \in Idx : Script[i].k = "prompt"}
Cancels == {i \in Idx : Script[i].k \in {"cancel", "cancelpeer"}}
PeerNotes == {i \in Idx : Script[i].k = "peernote"}
Sessions == {Script[i].s : i \in Prompts \cup Cancels}
None == 0

ASSUME \A i \in Idx : Script[i].k \in {"prompt", "cancel", "cancelpeer", "peernote"}
ASSUME \A i \in Idx : Script[i].after = 0 \/ (Script[i].after \in Prompts /\ Script[i].after < i)

VARIABLES
    read,           \* how many messages receive has read
    recvPc,         \* "idle", or "inflight": receive must still add prompt `read` to c.inflight
    lastEnq,        \* c.lastEnqueuedNotificationSeq
    completed,      \* c.completedNotificationSeq
    queue,          \* c.notificationQueue, as message indices
    seq,            \* sequence number of each notification, 0 until read
    procCur,        \* the notification whose handler runs, None if none
    procPc,         \* "idle", "peer" (handler waits on the client) or "complete"
    peerWaits,      \* the seqs with c.peerWaits[seq] > 0
    pc,             \* each prompt's goroutine
    wm,             \* each prompt's watermark (handlingRequest.watermark)
    canceled,       \* each prompt's reqCtx (the WithCancel in agent_gen.go) was canceled
    wrong,          \* prompts canceled by a session/cancel that arrived before them
    sessionCancels, \* a.sessionCancels: session -> prompt, None if absent
    markers,        \* a.sessionCancelledAt: session -> seq, 0 if absent
    handled         \* cancels whose cancelPrompt has run

vars == <<read, recvPc, lastEnq, completed, queue, seq, procCur, procPc, peerWaits,
          pc, wm, canceled, wrong, sessionCancels, markers, handled>>

PromptPcs == {"unread", "recv", "wait", "passed", "running", "cleanup", "respond", "exit", "done"}

\* receive adds the prompt to c.inflight and c.inflightWatermarks before it
\* starts the goroutine. The goroutine's defer removes it.
Inflight(p) == pc[p] \in {"wait", "passed", "running", "cleanup", "respond", "exit"}
Responded(p) == pc[p] \in {"exit", "done"}

Min(S) == CHOOSE x \in S : \A y \in S : x <= y

\* pruneCancelMarkers, with the in-flight requests of this state. The code
\* reads oldestInflightWatermark under c.mu, not a.mu, so the set can change
\* between that read and the deletes. One step is still exact: a request that
\* receive adds later has a watermark at or above every recorded seq, and a
\* request that leaves can only drop more markers on a later prune.
Prune(m) ==
    LET inf == {p \in Prompts : Inflight(p)}
        Drops(at) ==
            \/ inf = {}
            \/ PruneDropsWhen = "always"
            \/ PruneDropsWhen = "ge" /\ Min({wm[p] : p \in inf}) >= at
            \/ PruneDropsWhen = "gt" /\ Min({wm[p] : p \in inf}) > at
    IN [s \in Sessions |-> IF m[s] # 0 /\ Drops(m[s]) THEN 0 ELSE m[s]]

MarkerFires(at, w) ==
    \/ MarkerCancelsWhen = "gt" /\ at > w
    \/ MarkerCancelsWhen = "ge" /\ at >= w

Init ==
    /\ read = 0
    /\ recvPc = "idle"
    /\ lastEnq = 0
    /\ completed = 0
    /\ queue = <<>>
    /\ seq = [i \in Idx |-> 0]
    /\ procCur = None
    /\ procPc = "idle"
    /\ peerWaits = {}
    /\ pc = [p \in Prompts |-> "unread"]
    /\ wm = [p \in Prompts |-> 0]
    /\ canceled = [p \in Prompts |-> FALSE]
    /\ wrong = {}
    /\ sessionCancels = [s \in Sessions |-> None]
    /\ markers = [s \in Sessions |-> 0]
    /\ handled = {}

CanRead(i) ==
    /\ recvPc = "idle"
    /\ read + 1 = i
    /\ IF Script[i].after = 0 THEN TRUE ELSE Responded(Script[i].after)

\* receive reads a request: watermark := c.lastEnqueuedNotificationSeq.
ReceivePrompt ==
    \E i \in Prompts :
        /\ CanRead(i)
        /\ read' = i
        /\ recvPc' = "inflight"
        /\ wm' = [wm EXCEPT ![i] = lastEnq]
        /\ pc' = [pc EXCEPT ![i] = "recv"]
        /\ UNCHANGED <<lastEnq, completed, queue, seq, procCur, procPc, peerWaits,
                       canceled, wrong, sessionCancels, markers, handled>>

\* receive, under c.mu: c.inflight[idKey] = cancel, c.inflightWatermarks[watermark]++,
\* then go func(...).
ReceiveAddsInflight ==
    /\ recvPc = "inflight"
    /\ recvPc' = "idle"
    /\ pc' = [pc EXCEPT ![read] = "wait"]
    /\ UNCHANGED <<read, lastEnq, completed, queue, seq, procCur, procPc, peerWaits,
                   wm, canceled, wrong, sessionCancels, markers, handled>>

\* receive queues a notification under notifyMu.
ReceiveNotification ==
    \E i \in Cancels \cup PeerNotes :
        /\ CanRead(i)
        /\ read' = i
        /\ lastEnq' = lastEnq + 1
        /\ seq' = [seq EXCEPT ![i] = lastEnq + 1]
        /\ queue' = Append(queue, i)
        /\ UNCHANGED <<recvPc, completed, procCur, procPc, peerWaits, pc, wm,
                       canceled, wrong, sessionCancels, markers, handled>>

\* processNotifications takes a session/cancel. Its handler runs cancelPrompt
\* under a.mu: cancel and delete a.sessionCancels[s] unless that prompt came
\* after the cancel, record the marker, prune.
\* Then a.agent.Cancel runs with the handler's ctx. For "cancelpeer" it calls
\* the client, and startPeerWait counts c.peerWaits[seq].
RunCancel ==
    /\ procCur = None
    /\ queue # <<>>
    /\ Head(queue) \in Cancels
    /\ LET c == Head(queue)
           s == Script[c].s
           q == seq[c]
           t == sessionCancels[s]
           fire == t # None /\ (IF CancelSkipsLaterPrompt THEN wm[t] < q ELSE TRUE)
           m == IF CancelRecordsMarker THEN [markers EXCEPT ![s] = q] ELSE markers
       IN /\ canceled' = IF fire THEN [canceled EXCEPT ![t] = TRUE] ELSE canceled
          /\ wrong' = IF fire /\ q <= wm[t] THEN wrong \cup {t} ELSE wrong
          /\ sessionCancels' = IF fire THEN [sessionCancels EXCEPT ![s] = None] ELSE sessionCancels
          /\ markers' = Prune(m)
          /\ handled' = handled \cup {c}
          /\ procCur' = c
          /\ IF Script[c].k = "cancelpeer"
             THEN procPc' = "peer" /\ peerWaits' = peerWaits \cup {q}
             ELSE procPc' = "complete" /\ peerWaits' = peerWaits
          /\ queue' = Tail(queue)
    /\ UNCHANGED <<read, recvPc, lastEnq, completed, seq, pc, wm>>

\* processNotifications takes the extension notification. Its handler calls
\* the client with its ctx: startPeerWait counts c.peerWaits[seq].
StartPeerNote ==
    /\ procCur = None
    /\ queue # <<>>
    /\ Head(queue) \in PeerNotes
    /\ procCur' = Head(queue)
    /\ procPc' = "peer"
    /\ peerWaits' = peerWaits \cup {seq[Head(queue)]}
    /\ queue' = Tail(queue)
    /\ UNCHANGED <<read, recvPc, lastEnq, completed, seq, pc, wm, canceled, wrong,
                   sessionCancels, markers, handled>>

\* The client answers the handler's request: donePeerWait.
ClientAnswers ==
    /\ procPc = "peer"
    /\ procPc' = "complete"
    /\ peerWaits' = peerWaits \ {seq[procCur]}
    /\ UNCHANGED <<read, recvPc, lastEnq, completed, queue, seq, procCur, pc, wm,
                   canceled, wrong, sessionCancels, markers, handled>>

\* The handler returns: completedNotificationSeq = queued.seq under notifyMu.
CompleteNotification ==
    /\ procPc = "complete"
    /\ completed' = seq[procCur]
    /\ procCur' = None
    /\ procPc' = "idle"
    /\ UNCHANGED <<read, recvPc, lastEnq, queue, seq, peerWaits, pc, wm, canceled,
                   wrong, sessionCancels, markers, handled>>

\* waitInboundBarrier: waitNotifications(ctx, watermark, true) returns.
PassBarrier(p) ==
    /\ pc[p] = "wait"
    /\ \/ completed >= wm[p]
       \/ InboundStopsAtPeerWait /\ (completed + 1) \in peerWaits
    /\ pc' = [pc EXCEPT ![p] = "passed"]
    /\ UNCHANGED <<read, recvPc, lastEnq, completed, queue, seq, procCur, procPc,
                   peerWaits, wm, canceled, wrong, sessionCancels, markers, handled>>

\* The session/prompt case calls registerPrompt under a.mu: cancel the
\* previous prompt, store this one, cancel it if a later cancel already ran,
\* prune.
Register(p) ==
    /\ pc[p] = "passed"
    /\ LET s == Script[p].s
           prev == sessionCancels[s]
           hit == markers[s] # 0 /\ MarkerFires(markers[s], wm[p])
       IN /\ canceled' = [q \in Prompts |->
                             \/ canceled[q]
                             \/ q = prev
                             \/ q = p /\ hit]
          /\ wrong' = IF hit /\ markers[s] <= wm[p] THEN wrong \cup {p} ELSE wrong
          /\ sessionCancels' = [sessionCancels EXCEPT ![s] = p]
          /\ markers' = Prune(markers)
    /\ pc' = [pc EXCEPT ![p] = "running"]
    /\ UNCHANGED <<read, recvPc, lastEnq, completed, queue, seq, procCur, procPc,
                   peerWaits, wm, handled>>

\* a.agent.Prompt returns. An agent can return at any time.
PromptReturns(p) ==
    /\ pc[p] = "running"
    /\ pc' = [pc EXCEPT ![p] = "cleanup"]
    /\ UNCHANGED <<read, recvPc, lastEnq, completed, queue, seq, procCur, procPc,
                   peerWaits, wm, canceled, wrong, sessionCancels, markers, handled>>

\* The session/prompt case calls the func that registerPrompt returned. Under
\* a.mu it deletes a.sessionCancels[sessionId], only while the entry is this
\* prompt's when CleanupChecksOwner.
Cleanup(p) ==
    /\ pc[p] = "cleanup"
    /\ sessionCancels' =
           IF CleanupChecksOwner /\ sessionCancels[Script[p].s] # p
           THEN sessionCancels
           ELSE [sessionCancels EXCEPT ![Script[p].s] = None]
    /\ pc' = [pc EXCEPT ![p] = "respond"]
    /\ UNCHANGED <<read, recvPc, lastEnq, completed, queue, seq, procCur, procPc,
                   peerWaits, wm, canceled, wrong, markers, handled>>

\* handleInbound sends the response.
Respond(p) ==
    /\ pc[p] = "respond"
    /\ pc' = [pc EXCEPT ![p] = "exit"]
    /\ UNCHANGED <<read, recvPc, lastEnq, completed, queue, seq, procCur, procPc,
                   peerWaits, wm, canceled, wrong, sessionCancels, markers, handled>>

\* The goroutine's defer removes the request from c.inflight under c.mu.
Exit(p) ==
    /\ pc[p] = "exit"
    /\ pc' = [pc EXCEPT ![p] = "done"]
    /\ UNCHANGED <<read, recvPc, lastEnq, completed, queue, seq, procCur, procPc,
                   peerWaits, wm, canceled, wrong, sessionCancels, markers, handled>>

Finished ==
    /\ read = N
    /\ recvPc = "idle"
    /\ queue = <<>>
    /\ procCur = None
    /\ \A p \in Prompts : pc[p] = "done"

\* The client has sent everything and every handler returned.
AllDone == Finished /\ UNCHANGED vars

Next ==
    \/ ReceivePrompt
    \/ ReceiveAddsInflight
    \/ ReceiveNotification
    \/ RunCancel
    \/ StartPeerNote
    \/ ClientAnswers
    \/ CompleteNotification
    \/ \E p \in Prompts :
          \/ PassBarrier(p)
          \/ Register(p)
          \/ PromptReturns(p)
          \/ Cleanup(p)
          \/ Respond(p)
          \/ Exit(p)
    \/ AllDone

Spec == Init /\ [][Next]_vars

-----------------------------------------------------------------------------
TypeOK ==
    /\ read \in 0..N
    /\ recvPc \in {"idle", "inflight"}
    /\ procPc \in {"idle", "peer", "complete"}
    /\ completed <= lastEnq
    /\ pc \in [Prompts -> PromptPcs]
    /\ canceled \in [Prompts -> BOOLEAN]
    /\ wrong \subseteq Prompts
    /\ sessionCancels \in [Sessions -> Prompts \cup {None}]
    /\ handled \subseteq Cancels

\* A session/cancel cancels every prompt of its session that the client sent
\* before it, whichever handler runs first: once the cancel has run, a prompt
\* that is still in a.agent.Prompt has a canceled ctx.
CancelReachesEarlierPrompts ==
    \A p \in Prompts :
        (/\ pc[p] = "running"
         /\ \E c \in handled : Script[c].s = Script[p].s /\ seq[c] > wm[p])
        => canceled[p]

\* A session/cancel never cancels a prompt that the client sent after it.
\* Only cancels by cancelPrompt and by the marker count. registerPrompt's
\* cancel of the prompt that it replaces is not a session/cancel.
CancelSparesLaterPrompts == wrong = {}

-----------------------------------------------------------------------------
\* Scripts. Each config picks one with Script <- ...

\* A client that sends a prompt only after the previous one got its response,
\* to an agent whose Cancel does not call the client. It covers a cancel
\* before a prompt, a cancel right after a prompt (either handler can run
\* first), and a second prompt after the first one's response.
OnePromptAtATime ==
    << Msg("cancel", "a", 0),
       Msg("prompt", "a", 0),
       Msg("cancel", "a", 0),
       Msg("prompt", "a", 2),
       Msg("cancel", "a", 0) >>

\* Two sessions, one prompt at a time in each, with their prompts and cancels
\* interleaved, and no handler that calls the client. A cancel's prune runs while the other session's prompt is in
\* flight, and each session's second prompt comes after its first response.
TwoSessions ==
    << Msg("cancel", "a", 0),
       Msg("prompt", "a", 0),
       Msg("prompt", "b", 0),
       Msg("cancel", "b", 0),
       Msg("cancel", "a", 0),
       Msg("prompt", "b", 3),
       Msg("prompt", "a", 2),
       Msg("cancel", "a", 0),
       Msg("cancel", "b", 0) >>

\* A notification handler waits on the client while a cancel and a prompt
\* sent before it wait in line (#72 item 1).
PeerWaitThenPrompt ==
    << Msg("peernote", "-", 0),
       Msg("cancel", "a", 0),
       Msg("prompt", "a", 0) >>

\* The same release, with prompts sent before the cancels. A prompt that the
\* release starts early still gets the cancels sent after it. Some cancels'
\* Cancel calls the client too.
PeerWaitEarlierPrompt ==
    << Msg("prompt", "a", 0),
       Msg("peernote", "-", 0),
       Msg("cancelpeer", "a", 0),
       Msg("prompt", "b", 0),
       Msg("peernote", "-", 0),
       Msg("cancel", "b", 0),
       Msg("prompt", "a", 1),
       Msg("cancelpeer", "a", 0),
       Msg("cancel", "a", 0) >>

\* An agent whose Cancel calls the client. A second cancel and a prompt wait
\* behind the first cancel (#72 item 1, second trigger).
CancelWaitsThenPrompt ==
    << Msg("cancelpeer", "a", 0),
       Msg("cancel", "a", 0),
       Msg("prompt", "a", 0) >>

\* A prompt and a cancel right after it, for a /statechart chart (chart.cfg).
\* Either handler can run first.
PromptThenCancel ==
    << Msg("prompt", "a", 0),
       Msg("cancel", "a", 0) >>

\* Prompts of one session in flight at once, with cancels between them (#83).
\* A notification handler waits on the client, so the barrier can release a
\* later prompt early.
OverlappingPrompts ==
    << Msg("prompt", "a", 0),
       Msg("prompt", "a", 0),
       Msg("cancel", "a", 0),
       Msg("peernote", "-", 0),
       Msg("prompt", "a", 0),
       Msg("cancel", "a", 0) >>
=============================================================================
