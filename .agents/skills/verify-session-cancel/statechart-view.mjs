// Labels for reading SessionCancel.tla results with the /statechart skill.
// The words are what an agent author sees: the client's messages, the
// prompt handlers, and whether a prompt's ctx is canceled.

const ids = (f) => Object.keys(f).sort((a, b) => a - b);

const promptLabel = (pc, canceled) => {
  switch (pc) {
    case "unread":
      return "not sent";
    case "recv":
    case "wait":
      return "waits for earlier notifications";
    case "passed":
      return "starting";
    case "running":
      return canceled ? "running, ctx canceled" : "running";
    case "cleanup":
      return canceled ? "returned, ctx was canceled, entry not cleaned up" : "returned, entry not cleaned up";
    case "respond":
      return canceled ? "returned, ctx was canceled, response not sent" : "returned, response not sent";
    default:
      return canceled ? "answered, ctx was canceled" : "answered";
  }
};

const notificationsLabel = (v) => {
  if (v.procPc === "peer") return "a handler waits on the client";
  return Object.keys(v.queue).length > 0 ? "some queued" : "none queued";
};

const registryLabel = (v) =>
  ids(v.sessionCancels)
    .map((s) => {
      const entry = v.sessionCancels[s] === 0 ? "no prompt" : `prompt ${v.sessionCancels[s]}`;
      return v.markers[s] === 0 ? `${s}: ${entry}` : `${s}: ${entry}, cancel ${v.markers[s]} recorded`;
    })
    .join("; ");

export const title = "session/cancel and the prompts of a session";

export const state = (v) => {
  const regions = { Notifications: notificationsLabel(v), "Cancel registry": registryLabel(v) };
  for (const p of ids(v.pc)) regions[`Prompt ${p}`] = promptLabel(v.pc[p], v.canceled[p]);
  return regions;
};

const movedPrompt = (from, to) => ids(to.pc).find((p) => from.pc[p] !== to.pc[p]);

export const step = (action, from, to) => {
  const p = movedPrompt(from, to);
  switch (action) {
    case "ReceivePrompt":
      return `the client sends prompt ${p}`;
    case "ReceiveAddsInflight":
      return `the SDK marks prompt ${p} in flight`;
    case "ReceiveNotification":
      return "the client sends a notification";
    case "RunCancel":
      return "the cancel handler runs";
    case "StartPeerNote":
      return "a notification handler calls the client";
    case "ClientAnswers":
      return "the client answers the handler";
    case "CompleteNotification":
      return "the notification handler returns";
    case "PassBarrier":
      return `prompt ${p} passes the barrier`;
    case "Register":
      return `prompt ${p} registers its cancel func`;
    case "PromptReturns":
      return `the agent returns from prompt ${p}`;
    case "Cleanup":
      return `prompt ${p}'s cleanup deletes the session's entry`;
    case "Respond":
      return `prompt ${p}'s response goes out`;
    case "Exit":
      return `prompt ${p}'s handler exits`;
    case "AllDone":
      return null;
    default:
      return action;
  }
};
