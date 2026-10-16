const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const { createHash } = require('node:crypto');
const { prepareManifest } = require('./manifest.cjs');

test('relative logos are imported; outside files and bad digests are rejected', async t => {
  const dir = await fs.mkdtemp(path.join(os.tmpdir(), 'gerry-vite-'));
  t.after(() => fs.rm(dir, { recursive: true, force: true }));
  const root = path.join(dir, 'project'); await fs.mkdir(root);
  const file = path.join(root, 'gerrymander.yaml');
  await fs.writeFile(path.join(root, 'logo.svg'), '<svg/>');
  const write = logo => fs.writeFile(file, `project: demo\nzone: demo.test\napplication:\n  display:\n    logo: ${logo}\n`);
  let calls = 0;
  const upload = async data => { calls++; return { id: `sha256:${createHash('sha256').update(data).digest('hex')}` }; };
  await write('logo.svg'); assert.match(await prepareManifest(file, upload), /sha256:/); assert.equal(calls, 1);
  await fs.writeFile(path.join(dir, 'outside.svg'), '<svg/>');
  await fs.symlink(path.join(dir, 'outside.svg'), path.join(root, 'escape.svg'));
  for (const logo of ['../outside.svg', 'escape.svg', path.join(dir, 'outside.svg')]) {
    await write(logo); await assert.rejects(prepareManifest(file, upload));
  }
  assert.equal(calls, 1);
  await write('logo.svg'); await assert.rejects(prepareManifest(file, async () => ({ id: 'wrong' })), /digest/);
});
