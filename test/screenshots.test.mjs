import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { run } from '../src/util.mjs';
import { composeBeforeAfter, screenshotsMarkdown } from '../src/screenshots.mjs';

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'gwik-shot-'));

async function solid(file, color, size) {
  await run(
    'ffmpeg',
    ['-y', '-loglevel', 'error', '-f', 'lavfi', '-i', `color=c=${color}:s=${size}`, '-frames:v', '1', file],
    { allowFailure: false }
  );
}

test('composeBeforeAfter produces one padded side-by-side image', async () => {
  const before = path.join(tmp, 'before.png');
  const after = path.join(tmp, 'after.png');
  const out = path.join(tmp, 'combined.png');
  await solid(before, 'red', '100x50');
  await solid(after, 'blue', '80x70');
  await composeBeforeAfter(before, after, out);
  assert.equal(fs.existsSync(out), true);

  const probe = await run(
    'ffprobe',
    ['-v', 'error', '-select_streams', 'v:0', '-show_entries', 'stream=width,height', '-of', 'csv=p=0', out],
    { allowFailure: false }
  );
  const [width, height] = probe.stdout.trim().split(',').map(Number);
  // 100 + 12px gap + 80, padded to the taller 70px height
  assert.equal(width, 192);
  assert.equal(height, 70);
});

test('screenshotsMarkdown labels before/after', () => {
  const md = screenshotsMarkdown([
    { caption: '顶部工具栏', url: 'https://example.com/a.png' }
  ]);
  assert.match(md, /顶部工具栏/);
  assert.match(md, /左：改动前/);
  assert.match(md, /https:\/\/example.com\/a.png/);
  assert.equal(screenshotsMarkdown([]), null);
});
