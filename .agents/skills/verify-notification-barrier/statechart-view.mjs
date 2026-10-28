// Labels for reading NotificationBarrier.tla results with the /statechart skill.
// The words are what an SDK user sees: updates from the peer, handlers, and a
// request such as Prompt() that waits for them.

const callerLabel = (pc, result) => {
  switch (pc) {
    case "idle":
      return "not called";
    case "registered":
    case "waitResp":
      return "waiting for its response";
    case "ctxBranch":
    case "cleanupCancel":
    case "cleanupDisc":
      return "giving up";
    case "barrier":
    case "loop":
    case "checkDone":
    case "checkCtx":
    case "relock":
      return "has its response, checks earlier updates";
    case "waiting":
      return "has its response, asleep until a handler finishes";
    case "done":
      return result === "ok" ? "returned the response" : `returned ${result}`;
    default:
      return pc;
  }
};

const handlerLabel = (v) => {
  switch (v.workerPc) {
    case "dequeue":
      return "idle";
    case "handle":
      return v.cur.kind === "slow" ? "running a slow handler" : "running a handler";
    case "nestedCall":
      return "handler is calling a request";
    case "complete":
      return "marking the update done";
    case "exited":
      return "stopped";
    default:
      return v.workerPc;
  }
};

const connectionLabel = (v) => {
  if (!v.connDone) return "open";
  if (!v.inboundDone) return "closed, handlers still run";
  return "closed, handler ctx canceled";
};

export const state = (v) => {
  const regions = { Connection: connectionLabel(v), Handlers: handlerLabel(v) };
  for (const r of Object.keys(v.callerPc).sort()) {
    const name = r === "nested" ? "Request from a handler" : `Request ${r}`;
    regions[name] = callerLabel(v.callerPc[r], v.result[r]);
  }
  return regions;
};

const STEPS = {
  PeerSendsNote: "the peer sends an update",
  PeerResponds: "the peer answers a request",
  PeerCloses: "the peer closes the stream",
  RecvNotification: "the reader queues an update",
  RecvResponse: "the reader takes a response",
  RecvDeliver: "the reader hands over the response",
  RecvEOF: "the reader sees end of stream",
  ShutdownCancel: "the connection closes",
  ShutdownClose: "the reader closes the update queue",
  WorkerDequeue: "the handler goroutine takes the next update",
  WorkerExit: "the handler goroutine stops",
  PlainHandlerReturns: "a handler returns",
  SlowHandlerReturns: "a slow handler returns",
  NestedHandlerCalls: "a handler calls a request on the same connection",
  NestedHandlerReturns: "the handler's request returns",
  WorkerComplete: "the update is marked done",
  AppCalls: "the app sends a request",
  SendsRequest: "the request goes out",
  GetsResponse: "the request gets its response",
  SeesCtxDone: "the request sees its ctx canceled",
  CtxBranchChecksDone: "the request gives up",
  SeesDisconnect: "the request sees the disconnect",
  CleansUp: "the request returns an error",
  EntersBarrier: "the request waits for earlier updates",
  BarrierLoop: "the request checks earlier updates",
  BarrierChecksDone: "the request checks for a disconnect",
  BarrierChecksCtxOrWaits: "the request sleeps or gives up",
  BarrierRelocks: "the request wakes up",
  AppCancelsCtx: "the app cancels the request",
  CtxWakerFires: "the ctx cancel wakes sleepers",
  CtxWakerStops: null,
  ConnWatcherFires: "the disconnect wakes sleepers",
  DrainWakerFires: "the drain timeout wakes sleepers",
  DrainWakerStops: null,
  DrainStarts: "the drain starts",
  DrainLoop: "the drain checks handlers",
  DrainChecksTimeoutOrWaits: "the drain sleeps or times out",
  DrainRelocks: "the drain wakes up",
  InboundCancel: "handler ctx is canceled",
  DrainTimerFires: "5 s pass",
  Rest: null,
};

// The request that this step finished, if any.
const finished = (from, to) =>
  Object.keys(to.callerPc).some((r) => from.callerPc[r] !== "done" && to.callerPc[r] === "done");

const asleep = (from, to) =>
  Object.keys(to.callerPc).some((r) => from.callerPc[r] !== "waiting" && to.callerPc[r] === "waiting");

export const step = (action, from, to) => {
  if (action === "EntersBarrier" && finished(from, to)) return "the request returns: no earlier updates";
  if (action === "BarrierLoop" && finished(from, to)) return "the request returns: earlier updates are done";
  if (action === "BarrierChecksCtxOrWaits")
    return asleep(from, to) ? "the request sleeps until a handler finishes" : "the request gives up";
  return action in STEPS ? STEPS[action] : action;
};

export const group = (v) => (v.connDone ? (v.inboundDone ? "closed" : "closing") : "open");

export const colors = { open: "blue", closing: "orange", closed: "purple" };

export const title = "Notification barrier in connection.go";
