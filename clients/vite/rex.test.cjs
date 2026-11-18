const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs/promises");
const os = require("node:os");
const path = require("node:path");
const { EventEmitter } = require("node:events");
const gerry = require("./index.cjs");

test("Rex output keeps the remote label and HMR follows the browser", async t => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), "gerry-rex-vite-"));
  t.after(() => fs.rm(dir, { recursive: true, force: true }));
  await fs.writeFile(path.join(dir, "gerrymander.yaml"), "project: rexprobe\n");
  const previous = process.env;
  process.env = { ...previous, GERRY_REX_HOST: "mini", REX_SESSION: "test" };
  t.after(() => { process.env = previous; });
  t.mock.method(global, "fetch", async () => ({
    ok: true, json: async () => ({ services: { web: { hostnames: ["drive.example.test"], port: 51111 } } }),
  }));
  const plugin = gerry();
  const { server } = await plugin.config({ root: dir }, { command: "serve" });
  assert.equal(server.hmr.host, undefined);
  assert.equal(server.origin, undefined);
  const httpServer = new EventEmitter();
  const ready = new Promise(resolve => {
    plugin.configureServer({ httpServer, config: { logger: { info: resolve } } });
  });
  httpServer.emit("listening");
  const line = await ready;
  assert.ok(line.includes("https://drive.example.test/"));
  assert.equal(line.includes("gerry://mini/drive.example.test/"), Boolean(process.stdout.isTTY));
});
