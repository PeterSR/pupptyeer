// Conformance runner (TypeScript/Node) - implements conformance/scenario.md.
import { PupptyeerClient } from "../clients/typescript/index.mjs";

const sock = process.env.PUPPTYEER_SOCK;
function fail(msg) { console.error("FAIL[ts] " + msg); process.exit(1); }
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
if (!sock) fail("PUPPTYEER_SOCK not set");

const marker = "TS-" + process.hrtime.bigint();

const c = await PupptyeerClient.connect(sock);
const id = await c.newSession({ command: "cat", cols: 80, rows: 24 });
if (!id) fail("empty session id");

let acc = Buffer.alloc(0);
c.onOutput(id, (b) => { acc = Buffer.concat([acc, b]); });
await c.attach(id, { cols: 80, rows: 24 });
c.writePane(id, marker + "\n");

let deadline = Date.now() + 3000;
while (Date.now() < deadline && !acc.includes(marker)) await sleep(40);
if (!acc.toString().includes(marker)) fail("marker not in live output");

const cap = await c.capturePane(id);
if (!cap.toString().includes(marker)) fail("capture missing marker");

const sessions = await c.listSessions();
if (!sessions.find((s) => s.id === id)) fail("session not listed");

c.detach(id);

// reattach with a second connection → scrollback replay
const b = await PupptyeerClient.connect(sock);
let racc = Buffer.alloc(0);
b.onOutput(id, (buf) => { racc = Buffer.concat([racc, buf]); });
await b.attach(id, { cols: 80, rows: 24 });
deadline = Date.now() + 3000;
while (Date.now() < deadline && !racc.includes(marker)) await sleep(40);
if (!racc.toString().includes(marker)) fail("scrollback replay missing marker");

await b.kill(id);
deadline = Date.now() + 2000;
let gone = false;
while (Date.now() < deadline) {
  const ss = await c.listSessions();
  if (!ss.find((s) => s.id === id)) { gone = true; break; }
  await sleep(40);
}
if (!gone) fail("session still listed after kill");

// gc: a fresh session reaped by gc(0) (reap all idle sessions).
const id2 = await c.newSession({ command: "cat", cols: 80, rows: 24 });
const reaped = await c.gc(0);
if (!reaped.find((s) => s.id === id2)) fail("gc did not report reaping the session");
deadline = Date.now() + 2000;
gone = false;
while (Date.now() < deadline) {
  const ss = await c.listSessions();
  if (!ss.find((s) => s.id === id2)) { gone = true; break; }
  await sleep(40);
}
if (!gone) fail("session still listed after gc");

c.close();
b.close();
console.log("OK ts");
process.exit(0);
