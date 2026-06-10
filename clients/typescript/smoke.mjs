// Smoke test for the Node client. Requires a running daemon.
//   PUPPTYEER_SOCK=/tmp/pupptyeer-e2e.sock node smoke.mjs
import { PupptyeerClient } from "./index.mjs";

const sock = process.env.PUPPTYEER_SOCK || "/tmp/pupptyeer-e2e.sock";
const c = await PupptyeerClient.connect(sock);

const marker = "TS-MARKER-" + Date.now();
const id = await c.newSession({ command: "cat", cols: 80, rows: 24 });

let acc = Buffer.alloc(0);
c.onOutput(id, (b) => { acc = Buffer.concat([acc, b]); });
await c.attach(id, { cols: 80, rows: 24 });
c.writePane(id, marker + "\n");

const deadline = Date.now() + 3000;
while (Date.now() < deadline && !acc.includes(marker)) {
  await new Promise((r) => setTimeout(r, 50));
}
if (!acc.toString().includes(marker)) throw new Error("did not see marker in live output");

const cap = await c.capturePane(id);
if (!cap.toString().includes(marker)) throw new Error("capture missing marker");

const sessions = await c.listSessions();
if (!sessions.find((s) => s.id === id)) throw new Error("session not listed");

await c.kill(id);
c.close();
console.log("TS client smoke: OK (marker seen live + in capture, listed, killed)");
