#!/usr/bin/env node
// ts-client.mjs drives an ACP agent with the official TypeScript SDK
// (@agentclientprotocol/sdk), the way its own example client does: one
// initialize, one session/new and one prompt turn, with every permission
// request answered allow, and then the prompt /compact. It advertises form and
// url elicitation, fills each form field with "ts-host" and accepts each url.
// It advertises notices and compaction too. It records every JSON-RPC line in
// both directions.
//
//   node ts-client.mjs --out DIR --cwd DIR -- AGENT [ARGS...]
//
// It writes DIR/transcript.jsonl, DIR/agent.stderr.log, DIR/summary.json and
// DIR/result.txt. Exit 0: protocol version 1 was negotiated, the turn ended
// with end_turn, at least one agent message arrived, the agent asked for
// permission and got allow, the agent asked a form elicitation whose answer
// reached its edit and a url elicitation that it completed, the agent sent
// one notice and named each tool call, /compact streamed compaction updates
// with one id and ended with end_turn, no error
// response or non-JSON line crossed the wire, and the agent exited 0 after
// its input closed. Exit 1: otherwise. Run it from a folder whose
// node_modules holds the SDK.
import { spawn } from "node:child_process";
import { createWriteStream, mkdirSync, writeFileSync } from "node:fs";
import { Readable, Writable } from "node:stream";
import { finished } from "node:stream/promises";
import * as acp from "@agentclientprotocol/sdk";

const argv = process.argv.slice(2);
const flag = (name) => {
  const i = argv.indexOf(name);
  return i >= 0 && i < argv.indexOf("--") ? argv[i + 1] : undefined;
};
const out = flag("--out");
const cwd = flag("--cwd");
const agent = argv.slice(argv.indexOf("--") + 1);
if (!out || !cwd || argv.indexOf("--") < 0 || agent.length === 0) {
  console.error("usage: ts-client.mjs --out DIR --cwd DIR -- AGENT [ARGS...]");
  process.exit(2);
}
mkdirSync(out, { recursive: true });
mkdirSync(cwd, { recursive: true });

const start = Date.now();
const transcript = createWriteStream(`${out}/transcript.jsonl`);
// A write error fails the drive instead of crashing it.
const transcriptDone = finished(transcript).then(
  () => null,
  (err) => err,
);
const problems = [];
const kinds = {};
const permissions = [];
// The client answers the agent's elicitations: a form with tsHost as the
// value of each field, and a url with accept. elicitations lists each one.
const tsHost = "ts-host";
const elicitations = [];
// notices, toolNames and compaction record what the TypeScript SDK decoded of
// the notice, tool call and compaction updates.
const notices = [];
const toolNames = [];
const compaction = [];

function record(dir, line) {
  const entry = { t_ms: Date.now() - start, dir };
  try {
    entry.msg = JSON.parse(line);
    if (entry.msg.error) {
      problems.push(`${dir} error response: ${JSON.stringify(entry.msg.error)}`);
    }
  } catch {
    entry.raw = line;
    problems.push(`${dir} non-JSON line: ${line.slice(0, 200)}`);
  }
  transcript.write(JSON.stringify(entry) + "\n");
}

// tap passes bytes through unchanged and records each complete line.
function tap(dir) {
  const decoder = new TextDecoder();
  let buf = "";
  return new TransformStream({
    transform(chunk, controller) {
      buf += decoder.decode(chunk, { stream: true });
      for (let i = buf.indexOf("\n"); i >= 0; i = buf.indexOf("\n")) {
        const line = buf.slice(0, i);
        buf = buf.slice(i + 1);
        if (line.trim()) record(dir, line);
      }
      controller.enqueue(chunk);
    },
    // A fragment without a final newline is still a line on the wire.
    flush() {
      if (buf.trim()) record(dir, buf);
    },
  });
}

// The agent leads its own process group, so a signal reaches every
// process that it started.
const child = spawn(agent[0], agent.slice(1), { cwd, stdio: ["pipe", "pipe", "pipe"], detached: true });
const signalGroup = (sig) => {
  try {
    process.kill(-child.pid, sig);
  } catch {}
};
const agentLog = createWriteStream(`${out}/agent.stderr.log`);
child.stderr.pipe(agentLog);
// "close" fires after the agent exits and its stdio streams end, so every
// stderr line has reached agentLog by then. "exit" fires when it dies.
const closed = new Promise((resolve) => child.on("close", (code, signal) => resolve(signal ?? `exit ${code}`)));
const died = new Promise((resolve) => child.on("exit", () => resolve(true)));

const toAgent = tap("client->agent");
toAgent.readable.pipeTo(Writable.toWeb(child.stdin)).catch(() => {});
const fromAgent = Readable.toWeb(child.stdout).pipeThrough(tap("agent->client"));
const stream = acp.ndJsonStream(toAgent.writable, fromAgent);

let outcome;
try {
  outcome = await acp
    .client({ name: "acp-harness-ts-client" })
    .onRequest(acp.methods.client.session.requestPermission, (ctx) => {
      const opts = ctx.params.options;
      const pick = opts.find((o) => o.kind === "allow_once") ?? opts.find((o) => o.kind === "allow_always");
      toolNames.push(`permission ${ctx.params.toolCall.toolCallId}=${ctx.params.toolCall.name ?? "<none>"}`);
      permissions.push({
        title: ctx.params.toolCall.title ?? "",
        content: ctx.params.toolCall.rawInput?.content ?? "",
        answer: pick ? pick.optionId : "cancelled",
      });
      return pick ? { outcome: { outcome: "selected", optionId: pick.optionId } } : { outcome: { outcome: "cancelled" } };
    })
    .onRequest(acp.methods.client.elicitation.create, (ctx) => {
      const p = ctx.params;
      if (p.mode === "form") {
        const content = Object.fromEntries(Object.keys(p.requestedSchema.properties ?? {}).map((k) => [k, tsHost]));
        elicitations.push({ mode: "form", fields: Object.keys(content), toolCallId: p.toolCallId ?? null });
        return { action: "accept", content };
      }
      elicitations.push({ mode: p.mode, elicitationId: p.elicitationId ?? null });
      return p.mode === "url" ? { action: "accept" } : { action: "decline" };
    })
    .onNotification(acp.methods.client.elicitation.complete, (ctx) => {
      elicitations.push({ complete: ctx.params.elicitationId });
    })
    .onNotification(acp.methods.client.session.update, (ctx) => {
      const u = ctx.params.update;
      const kind = u.sessionUpdate;
      kinds[kind] = (kinds[kind] ?? 0) + 1;
      if (kind === "notice") notices.push({ severity: u.severity, title: u.title, description: u.description ?? null });
      if (kind === "tool_call") toolNames.push(`${u.toolCallId}=${u.name ?? "<none>"}`);
      if (kind === "compaction_update") compaction.push({ kind, id: u.compactionId, status: u.status });
      if (kind === "compaction_summary_chunk") compaction.push({ kind, id: u.compactionId, text: u.content?.text ?? null });
    })
    .connectWith(stream, async (ctx) => {
      const init = await ctx.request(acp.methods.agent.initialize, {
        protocolVersion: acp.PROTOCOL_VERSION,
        clientCapabilities: { elicitation: { form: {}, url: {} }, session: { notices: {}, compaction: {} } },
        clientInfo: { name: "acp-harness-ts-client", version: "0" },
      });
      const session = await ctx.request(acp.methods.agent.session.new, { cwd, mcpServers: [] });
      const turn = await ctx.request(acp.methods.agent.session.prompt, {
        sessionId: session.sessionId,
        prompt: [{ type: "text", text: "Hello, agent!" }],
      });
      const compact = await ctx.request(acp.methods.agent.session.prompt, {
        sessionId: session.sessionId,
        prompt: [{ type: "text", text: "/compact" }],
      });
      return {
        protocolVersion: init.protocolVersion,
        sessionId: session.sessionId,
        stopReason: turn.stopReason,
        compactStopReason: compact.stopReason,
      };
    });
} catch (err) {
  problems.push(`request failed: ${err?.message ?? err}`);
}

child.stdin.end();
const within = (p, ms) => Promise.race([p, new Promise((r) => setTimeout(() => r(null), ms))]);
let exit = await within(closed, 5000);
if (exit !== null) {
  await new Promise((r) => (agentLog.writableFinished ? r() : agentLog.on("finish", r)));
} else {
  // Nothing else would stop an agent that outlives this client.
  signalGroup("SIGTERM");
  exit = (await within(died, 5000)) ? "stopped with SIGTERM after 5 s" : null;
  signalGroup("SIGKILL");
  exit ??= (await within(died, 5000)) ? "killed after SIGTERM was ignored" : "still running after SIGKILL";
}

// The transcript closes before result.txt is written, so that result.txt
// never reports a pass whose transcript is incomplete.
transcript.end();
const transcriptErr = await transcriptDone;
const checks = [
  [`protocol version ${acp.PROTOCOL_VERSION} negotiated`, outcome?.protocolVersion === acp.PROTOCOL_VERSION],
  ["session/new returned a session id", typeof outcome?.sessionId === "string" && outcome.sessionId !== ""],
  ["turn ended with end_turn", outcome?.stopReason === "end_turn"],
  ["at least one agent_message_chunk", (kinds.agent_message_chunk ?? 0) > 0],
  ["every permission request allowed", permissions.length > 0 && permissions.every((p) => p.answer !== "cancelled")],
  [
    "form elicitation answered, and the edit carries its value",
    elicitations[0]?.mode === "form" && permissions.length > 0 && permissions.every((p) => p.content.includes(tsHost)),
  ],
  [
    "url elicitation accepted, then completed with its id",
    elicitations.length === 3 &&
      elicitations[1].mode === "url" &&
      typeof elicitations[1].elicitationId === "string" &&
      elicitations[2].complete === elicitations[1].elicitationId,
  ],
  [
    "one info notice",
    notices.length === 1 && notices[0].severity === "info" && notices[0].title !== "",
  ],
  [
    "each tool call and the permission request carry a name",
    toolNames.join(" ") === "call_1=read_file call_2=write_file permission call_2=write_file",
  ],
  [
    "/compact streamed compaction updates with one id and ended with end_turn",
    outcome?.compactStopReason === "end_turn" &&
      compaction.map((c) => c.status ?? c.kind).join(" ") === "in_progress compaction_summary_chunk completed" &&
      new Set(compaction.map((c) => c.id)).size === 1 &&
      typeof compaction[1]?.text === "string",
  ],
  ["no error response or non-JSON line on the wire", problems.length === 0],
  ["agent exited 0 after its input closed", exit === "exit 0"],
  [`transcript.jsonl written${transcriptErr ? `: ${transcriptErr.message}` : ""}`, transcriptErr === null],
];
const pass = checks.every(([, ok]) => ok);
const summary = {
  agent,
  sdk: "@agentclientprotocol/sdk",
  outcome,
  kinds,
  permissions,
  elicitations,
  notices,
  toolNames,
  compaction,
  problems,
  agentExit: exit,
  pass,
};
writeFileSync(`${out}/summary.json`, JSON.stringify(summary, null, 2) + "\n");
const lines = checks.map(([name, ok]) => `${ok ? "PASS" : "FAIL"}  ${name}`);
lines.push(...problems.map((p) => `      ${p}`), `exit=${pass ? 0 : 1}`);
writeFileSync(`${out}/result.txt`, lines.join("\n") + "\n");
console.log(lines.join("\n"));
process.exit(pass ? 0 : 1);
