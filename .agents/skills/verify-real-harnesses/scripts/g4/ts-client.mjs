#!/usr/bin/env node
// ts-client.mjs drives an ACP agent with the official TypeScript SDK
// (@agentclientprotocol/sdk), the way its own example client does: one
// initialize, one session/new and one prompt turn, with every permission
// request answered allow. It records every JSON-RPC line in both directions.
//
//   node ts-client.mjs --out DIR --cwd DIR -- AGENT [ARGS...]
//
// It writes DIR/transcript.jsonl, DIR/agent.stderr.log, DIR/summary.json and
// DIR/result.txt. Exit 0: the turn ended with end_turn, at least one agent
// message arrived, and no error response or non-JSON line crossed the wire.
// Exit 1: otherwise. Run it from a folder whose node_modules holds the SDK.
import { spawn } from "node:child_process";
import { createWriteStream, mkdirSync, writeFileSync } from "node:fs";
import { Readable, Writable } from "node:stream";
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
const problems = [];
const kinds = {};
const permissions = [];

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
  });
}

const child = spawn(agent[0], agent.slice(1), { cwd, stdio: ["pipe", "pipe", "pipe"] });
child.stderr.pipe(createWriteStream(`${out}/agent.stderr.log`));
const exited = new Promise((resolve) => child.on("exit", (code, signal) => resolve(signal ?? `exit ${code}`)));

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
      permissions.push({ title: ctx.params.toolCall.title ?? "", answer: pick ? pick.optionId : "cancelled" });
      return pick ? { outcome: { outcome: "selected", optionId: pick.optionId } } : { outcome: { outcome: "cancelled" } };
    })
    .onNotification(acp.methods.client.session.update, (ctx) => {
      const kind = ctx.params.update.sessionUpdate;
      kinds[kind] = (kinds[kind] ?? 0) + 1;
    })
    .connectWith(stream, async (ctx) => {
      const init = await ctx.request(acp.methods.agent.initialize, {
        protocolVersion: acp.PROTOCOL_VERSION,
        clientCapabilities: {},
        clientInfo: { name: "acp-harness-ts-client", version: "0" },
      });
      const session = await ctx.request(acp.methods.agent.session.new, { cwd, mcpServers: [] });
      const turn = await ctx.request(acp.methods.agent.session.prompt, {
        sessionId: session.sessionId,
        prompt: [{ type: "text", text: "Hello, agent!" }],
      });
      return { protocolVersion: init.protocolVersion, sessionId: session.sessionId, stopReason: turn.stopReason };
    });
} catch (err) {
  problems.push(`request failed: ${err?.message ?? err}`);
}

child.stdin.end();
const exit = await Promise.race([exited, new Promise((r) => setTimeout(() => r(null), 5000))]);
if (exit === null) child.kill("SIGTERM");

const checks = [
  ["turn ended with end_turn", outcome?.stopReason === "end_turn"],
  ["at least one agent_message_chunk", (kinds.agent_message_chunk ?? 0) > 0],
  ["no error response or non-JSON line on the wire", problems.length === 0],
];
const pass = checks.every(([, ok]) => ok);
const summary = { agent, sdk: "@agentclientprotocol/sdk", outcome, kinds, permissions, problems, agentExit: exit ?? "killed", pass };
writeFileSync(`${out}/summary.json`, JSON.stringify(summary, null, 2) + "\n");
const lines = checks.map(([name, ok]) => `${ok ? "PASS" : "FAIL"}  ${name}`);
lines.push(...problems.map((p) => `      ${p}`), `exit=${pass ? 0 : 1}`);
writeFileSync(`${out}/result.txt`, lines.join("\n") + "\n");
await new Promise((r) => transcript.end(r));
console.log(lines.join("\n"));
process.exit(pass ? 0 : 1);
