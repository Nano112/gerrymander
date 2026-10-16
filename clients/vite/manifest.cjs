const fs = require('node:fs/promises');
const path = require('node:path');
const { createHash } = require('node:crypto');
const YAML = require('yaml');

// Import only explicitly declared display logos. Never traverse supervised env
// or interpret arbitrary strings as paths. The server validates image content.
async function prepareManifest(filename, upload) {
  const doc = YAML.parse(await fs.readFile(filename, 'utf8'));
  if (!doc || typeof doc !== 'object') throw new Error('Invalid manifest');
  const base = await fs.realpath(path.dirname(filename));
  const displays = [doc.application?.display,
    ...Object.values(doc.services || {}).map(s => s?.display),
    ...Object.values(doc.resources || {}).map(r => r?.display)];
  for (const display of displays) {
    if (!display?.logo || /^sha256:[a-f0-9]{64}$/.test(display.logo)) continue;
    if (typeof display.logo !== 'string' || path.isAbsolute(display.logo)) throw new Error('Logo must be relative to the manifest directory');
    const resolved = await fs.realpath(path.resolve(base, display.logo));
    const relative = path.relative(base, resolved);
    if (relative === '..' || relative.startsWith(`..${path.sep}`) || path.isAbsolute(relative)) throw new Error('Logo escapes manifest directory');
    const handle = await fs.open(resolved, 'r');
    let data;
    try {
      const stat = await handle.stat();
      if (!stat.isFile() || stat.size === 0 || stat.size > 512 * 1024) throw new Error('Logo must be a regular file up to 512 KiB');
      // Bound reads even if another process grows the file after stat.
      const buffer = Buffer.alloc(512 * 1024 + 1);
      let offset = 0;
      while (offset < buffer.length) {
        const { bytesRead } = await handle.read(buffer, offset, buffer.length - offset, null);
        if (!bytesRead) break;
        offset += bytesRead;
      }
      if (offset > 512 * 1024) throw new Error('Logo exceeds 512 KiB');
      data = buffer.subarray(0, offset);
    } finally { await handle.close(); }
    const expected = `sha256:${createHash('sha256').update(data).digest('hex')}`;
    const asset = await upload(data);
    if (asset.id !== expected) throw new Error('Logo upload returned unexpected digest');
    display.logo = asset.id;
  }
  return YAML.stringify(doc);
}

module.exports = { prepareManifest };
