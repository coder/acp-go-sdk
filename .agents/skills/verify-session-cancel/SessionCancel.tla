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
    MarkerCancelsWhen,      \* registerPrompt: "gt" (cancelledAt > arrival), "ge", or "never"
    CancelRecordsMarker,    \* cancelPrompt stores sessionCancelledAt[sessionID] = arrival
    MarkerKeepsLatest,      \* cancelPrompt stores it only when it is later than the stored one
    PruneDropsWhen,         \* pruneCancelMarkers: "ge" (oldest >= cancelledAt), "gt", or "always"
    CancelSkipsLaterPrompt, \* cancelPrompt leaves a registered prompt that arrived after the cancel
    CleanupChecksOwner,     \* the prompt cleanup deletes the session's entry only while it is its own
    NewestDropsWhen,        \* the sessionNewest prune: "ge" (oldest arrival >= newest) or "always"
    KeepNewerBy,            \* registerPrompt cancels an older prompt at once: "newest" (sessionNewest), "prev" (only the registered entry), or "never"
    InboundStopsAtPeerWait  \* waitInboundBarrier also ends while the running handler waits on the peer

ASSUME MarkerCancelsWhen \in {"gt", "ge", "never"}
ASSUME PruneDropsWhen \in {"ge", "gt", "always"}
ASSUME CancelRecordsMarker \in BOOLEAN /\ InboundStopsAtPeerWait \in BOOLEAN /\ MarkerKeepsLatest \in BOOLEAN
ASSUME CancelSkipsLaterPrompt \in BOOLEAN /\ CleanupChecksOwner \in BOOLEAN
ASSUME KeepNewerBy \in {"newest", "prev", "never"} /\ NewestDropsWhen \in {"ge", "always"}

\* One message from the client. k is "prompt", "cancel", "cancelpeer" (a
\* session/cancel whose a.agent.Cancel calls the client with its ctx),
\* "cancelreq" (a session/cancel sent as a request, with an id) or
\* "peernote" (an extension notification whose handler calls the client with
\* its ctx). s is the session. after > 0: the client sends it only once it has
\* the response to the prompt at that index.
Msg(k, s, after) == [k |-> k, s |-> s, after |-> after]

N == Len(Script)
Idx == 1..N
Prompts == {i \in Idx : Script[i].k = "prompt"}
Cancels == {i \in Idx : Script[i].k \in {"cancel", "cancelpeer"}}
CancelReqs == {i \in Idx : Script[i].k = "cancelreq"}
Requests == Prompts \cup CancelReqs
PeerNotes == {i \in Idx : Script[i].k = "peernote"}
Sessions == {Script[i].s : i \in Prompts \cup Cancels \cup CancelReqs}
None == 0

ASSUME \A i \in Idx : Script[i].k \in {"prompt", "cancel", "cancelpeer", "cancelreq", "peernote"}
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
    pc,             \* each request's goroutine (prompts and cancel requests)
    wm,             \* each request's barrier watermark in receive
    canceled,       \* each prompt's reqCtx (the WithCancel in agent_gen.go) was canceled
    wrong,          \* prompts canceled by a session/cancel that arrived before them
    replaced,       \* prompts canceled by registerPrompt for a prompt that arrived before them
    sessionCancels, \* a.sessionCancels: session -> prompt, None if absent
    markers,        \* a.sessionCancelledAt: session -> arrival (cancel index), 0 if absent
    newest,         \* a.sessionNewest: session -> arrival (prompt index), 0 if absent
    handled         \* cancels (notifications and requests) whose cancelPrompt has run

vars == <<read, recvPc, lastEnq, completed, queue, seq, procCur, procPc, peerWaits,
          pc, wm, canceled, wrong, replaced, sessionCancels, markers, newest, handled>>

PromptPcs == {"unread", "recv", "wait", "passed", "running", "cleanup", "respond", "exit", "done"}

\* Every message that receive dispatches gets the next arrival number
\* (c.inboundMessages), so a message's index in the script is its arrival.
\* receive adds a request to c.inflight and c.inflightArrivals before it
\* starts the goroutine. The goroutine's defer removes it.
Inflight(p) == pc[p] \in {"wait", "passed", "running", "cleanup", "respond", "exit"}
Responded(p) == pc[p] \in {"exit", "done"}

\* pruneCancelMarkers, with the in-flight requests of this state. The code
\* reads oldestInflightArrival under c.mu, not a.mu, so the set can change
\* between that read and the deletes. One step is still exact: a request that
\* receive adds later has an arrival above every recorded one, and a request
\* that leaves can only drop more entries on a later prune.
Prune(m) ==
    LET Drops(at) ==
            \/ PruneDropsWhen = "always"
            \/ PruneDropsWhen = "ge" /\ ~\E q \in Requests : Inflight(q) /\ q < at
            \/ PruneDropsWhen = "gt" /\ ~\E q \in Requests : Inflight(q) /\ q <= at
    IN [s \in Sessions |-> IF m[s] # 0 /\ Drops(m[s]) THEN 0 ELSE m[s]]

\* The sessionNewest half of pruneCancelMarkers.
PruneNewest(n) ==
    [s \in Sessions |->
        IF n[s] # 0 /\ (NewestDropsWhen = "always" \/ ~\E q \in Requests : Inflight(q) /\ q < n[s])
        THEN 0 ELSE n[s]]

MarkerFires(at, p) ==
    \/ MarkerCancelsWhen = "gt" /\ at > p
    \/ MarkerCancelsWhen = "ge" /\ at >= p

\* cancelPrompt for the session/cancel at index c, under a.mu: cancel and
\* delete a.sessionCancels[s] unless that prompt arrived after the cancel,
\* record the marker, prune.
DoCancel(c) ==
    LET s == Script[c].s
        t == sessionCancels[s]
        fire == t # None /\ (IF CancelSkipsLaterPrompt THEN t < c ELSE TRUE)
        m == IF CancelRecordsMarker /\ (~MarkerKeepsLatest \/ c > markers[s])
             THEN [markers EXCEPT ![s] = c] ELSE markers
    IN /\ canceled' = IF fire THEN [canceled EXCEPT ![t] = TRUE] ELSE canceled
       /\ wrong' = IF fire /\ c < t THEN wrong \cup {t} ELSE wrong
       /\ sessionCancels' = IF fire THEN [sessionCancels EXCEPT ![s] = None] ELSE sessionCancels
       /\ markers' = Prune(m)
       /\ newest' = PruneNewest(newest)
       /\ handled' = handled \cup {c}

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
    /\ pc = [p \in Requests |-> "unread"]
    /\ wm = [p \in Requests |-> 0]
    /\ canceled = [p \in Prompts |-> FALSE]
    /\ wrong = {}
    /\ replaced = {}
    /\ sessionCancels = [s \in Sessions |-> None]
    /\ markers = [s \in Sessions |-> 0]
    /\ newest = [s \in Sessions |-> 0]
    /\ handled = {}

CanRead(i) ==
    /\ recvPc = "idle"
    /\ read + 1 = i
    /\ IF Script[i].after = 0 THEN TRUE ELSE Responded(Script[i].after)

\* receive reads a request: watermark := c.lastEnqueuedNotificationSeq.
ReceivePrompt ==
    \E i \in Requests :
        /\ CanRead(i)
        /\ read' = i
        /\ recvPc' = "inflight"
        /\ wm' = [wm EXCEPT ![i] = lastEnq]
        /\ pc' = [pc EXCEPT ![i] = "recv"]
        /\ UNCHANGED <<lastEnq, completed, queue, seq, procCur, procPc, peerWaits,
                       canceled, wrong, replaced, sessionCancels, markers, newest, handled>>

\* receive, under c.mu: c.inflight[idKey] = cancel, c.inflightArrivals[arrival],
\* then go func(...).
ReceiveAddsInflight ==
    /\ recvPc = "inflight"
    /\ recvPc' = "idle"
    /\ pc' = [pc EXCEPT ![read] = "wait"]
    /\ UNCHANGED <<read, lastEnq, completed, queue, seq, procCur, procPc, peerWaits,
                   wm, canceled, wrong, replaced, sessionCancels, markers, newest, handled>>

\* receive queues a notification under notifyMu.
ReceiveNotification ==
    \E i \in Cancels \cup PeerNotes :
        /\ CanRead(i)
        /\ read' = i
        /\ lastEnq' = lastEnq + 1
        /\ seq' = [seq EXCEPT ![i] = lastEnq + 1]
        /\ queue' = Append(queue, i)
        /\ UNCHANGED <<recvPc, completed, procCur, procPc, peerWaits, pc, wm,
                       canceled, wrong, replaced, sessionCancels, markers, newest, handled>>

\* processNotifications takes a session/cancel. Its handler runs cancelPrompt.
\* Then a.agent.Cancel runs with the handler's ctx. For "cancelpeer" it calls
\* the client, and startPeerWait counts c.peerWaits[seq].
RunCancel ==
    /\ procCur = None
    /\ queue # <<>>
    /\ Head(queue) \in Cancels
    /\ LET c == Head(queue)
       IN /\ DoCancel(c)
          /\ procCur' = c
          /\ IF Script[c].k = "cancelpeer"
             THEN procPc' = "peer" /\ peerWaits' = peerWaits \cup {seq[c]}
             ELSE procPc' = "complete" /\ peerWaits' = peerWaits
          /\ queue' = Tail(queue)
    /\ UNCHANGED <<read, recvPc, lastEnq, completed, seq, pc, wm, replaced>>

\* A session/cancel sent as a request runs the same case on its own request
\* goroutine, after the barrier, and then sends its response.
RunCancelReq(r) ==
    /\ pc[r] = "passed"
    /\ r \in CancelReqs
    /\ DoCancel(r)
    /\ pc' = [pc EXCEPT ![r] = "respond"]
    /\ UNCHANGED <<read, recvPc, lastEnq, completed, queue, seq, procCur, procPc,
                   peerWaits, wm, replaced>>

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
    /\ UNCHANGED <<read, recvPc, lastEnq, completed, seq, pc, wm, canceled, wrong, replaced,
                   sessionCancels, markers, newest, handled>>

\* The client answers the handler's request: donePeerWait.
ClientAnswers ==
    /\ procPc = "peer"
    /\ procPc' = "complete"
    /\ peerWaits' = peerWaits \ {seq[procCur]}
    /\ UNCHANGED <<read, recvPc, lastEnq, completed, queue, seq, procCur, pc, wm,
                   canceled, wrong, replaced, sessionCancels, markers, newest, handled>>

\* The handler returns: completedNotificationSeq = queued.seq under notifyMu.
CompleteNotification ==
    /\ procPc = "complete"
    /\ completed' = seq[procCur]
    /\ procCur' = None
    /\ procPc' = "idle"
    /\ UNCHANGED <<read, recvPc, lastEnq, queue, seq, peerWaits, pc, wm, canceled,
                   wrong, replaced, sessionCancels, markers, newest, handled>>

\* waitInboundBarrier: waitNotifications(ctx, watermark, true) returns.
PassBarrier(p) ==
    /\ pc[p] = "wait"
    /\ \/ completed >= wm[p]
       \/ InboundStopsAtPeerWait /\ (completed + 1) \in peerWaits
    /\ pc' = [pc EXCEPT ![p] = "passed"]
    /\ UNCHANGED <<read, recvPc, lastEnq, completed, queue, seq, procCur, procPc,
                   peerWaits, wm, canceled, wrong, replaced, sessionCancels, markers, newest, handled>>

\* The session/prompt case calls registerPrompt under a.mu: cancel the
\* previous prompt, store this one, cancel it if a later cancel already ran,
\* prune.
Register(p) ==
    /\ pc[p] = "passed"
    /\ p \in Prompts
    /\ LET s == Script[p].s
           prev == sessionCancels[s]
           \* Prompts arrive in index order, so a greater index arrived
           \* later (messageArrival).
           keepPrev == CASE KeepNewerBy = "newest" -> newest[s] > p
                       [] KeepNewerBy = "prev" -> prev # None /\ prev > p
                       [] OTHER -> FALSE
           hit == markers[s] # 0 /\ MarkerFires(markers[s], p)
       IN IF keepPrev
          THEN /\ canceled' = [canceled EXCEPT ![p] = TRUE]
               /\ UNCHANGED <<wrong, replaced, sessionCancels, markers, newest>>
          ELSE /\ canceled' = [q \in Prompts |->
                                  \/ canceled[q]
                                  \/ q = prev
                                  \/ q = p /\ hit]
               /\ wrong' = IF hit /\ markers[s] < p THEN wrong \cup {p} ELSE wrong
               /\ replaced' = IF prev # None /\ prev > p THEN replaced \cup {prev} ELSE replaced
               /\ sessionCancels' = [sessionCancels EXCEPT ![s] = p]
               /\ markers' = Prune(markers)
               /\ newest' = PruneNewest(IF KeepNewerBy = "newest" THEN [newest EXCEPT ![s] = p] ELSE newest)
    /\ pc' = [pc EXCEPT ![p] = "running"]
    /\ UNCHANGED <<read, recvPc, lastEnq, completed, queue, seq, procCur, procPc,
                   peerWaits, wm, handled>>

\* a.agent.Prompt returns. An agent can return at any time.
PromptReturns(p) ==
    /\ pc[p] = "running"
    /\ pc' = [pc EXCEPT ![p] = "cleanup"]
    /\ UNCHANGED <<read, recvPc, lastEnq, completed, queue, seq, procCur, procPc,
                   peerWaits, wm, canceled, wrong, replaced, sessionCancels, markers, newest, handled>>

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
                   peerWaits, wm, canceled, wrong, replaced, markers, newest, handled>>

\* handleInbound sends the response.
Respond(p) ==
    /\ pc[p] = "respond"
    /\ pc' = [pc EXCEPT ![p] = "exit"]
    /\ UNCHANGED <<read, recvPc, lastEnq, completed, queue, seq, procCur, procPc,
                   peerWaits, wm, canceled, wrong, replaced, sessionCancels, markers, newest, handled>>

\* The goroutine's defer removes the request from c.inflight under c.mu.
Exit(p) ==
    /\ pc[p] = "exit"
    /\ pc' = [pc EXCEPT ![p] = "done"]
    /\ UNCHANGED <<read, recvPc, lastEnq, completed, queue, seq, procCur, procPc,
                   peerWaits, wm, canceled, wrong, replaced, sessionCancels, markers, newest, handled>>

Finished ==
    /\ read = N
    /\ recvPc = "idle"
    /\ queue = <<>>
    /\ procCur = None
    /\ \A p \in Requests : pc[p] = "done"

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
    \/ \E p \in Requests :
          \/ PassBarrier(p)
          \/ Register(p)
          \/ RunCancelReq(p)
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
    /\ pc \in [Requests -> PromptPcs]
    /\ canceled \in [Prompts -> BOOLEAN]
    /\ wrong \subseteq Prompts
    /\ replaced \subseteq Prompts
    /\ sessionCancels \in [Sessions -> Prompts \cup {None}]
    /\ handled \subseteq Cancels \cup CancelReqs

\* A session/cancel cancels every prompt of its session that the client sent
\* before it, whichever handler runs first: once the cancel has run, a prompt
\* that is still in a.agent.Prompt has a canceled ctx.
CancelReachesEarlierPrompts ==
    \A p \in Prompts :
        (/\ pc[p] = "running"
         /\ \E c \in handled : Script[c].s = Script[p].s /\ c > p)
        => canceled[p]

\* A session/cancel never cancels a prompt that the client sent after it.
\* Only cancels by cancelPrompt and by the marker count. registerPrompt's
\* cancel of the prompt that it replaces is not a session/cancel.
CancelSparesLaterPrompts == wrong = {}

\* When two prompts of one session overlap, registerPrompt never cancels the
\* one that the client sent later for the one that it sent first.
NewerPromptSurvives == replaced = {}

\* While a prompt of a session that the client sent later has registered, an
\* older prompt of the session never runs with a live ctx, also when the newer
\* one has already returned.
Registered == {"running", "cleanup", "respond", "exit", "done"}
OlderPromptEnds ==
    \A p, q \in Prompts :
        (Script[p].s = Script[q].s /\ p < q /\ pc[q] \in Registered /\ pc[p] = "running")
        => canceled[p]

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

\* session/cancel sent as a request, before and after prompts, with a
\* handler that waits on the client so the barrier can release requests early,
\* and a notification cancel among them.
CancelRequests ==
    << Msg("prompt", "a", 0),
       Msg("cancelreq", "a", 0),
       Msg("peernote", "-", 0),
       Msg("prompt", "a", 0),
       Msg("cancelreq", "a", 0),
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
