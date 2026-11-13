import test from 'node:test';
import assert from 'node:assert/strict';
import os from 'node:os';
import path from 'node:path';
import fs from 'node:fs';
import { loadConfig, normalizeRepos } from '../src/config.mjs';

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'gwik-config-'));

test('repos accept array or object and default approvers to the owner', () => {
  const repos = normalizeRepos(
    { 'me/one': {}, 'me/two': { agent: 'build' } },
    { approvers: ['me'] }
  );
  assert.equal(repos.length, 2);
  assert.deepEqual(repos[0].approvers, ['me']);
  assert.equal(repos[1].agent, 'build');
  assert.throws(() => normalizeRepos(['me/dup', 'me/dup']));
  assert.throws(() => normalizeRepos('not a repo'));
  assert.throws(() => normalizeRepos([]));
});

test('loadConfig reads a full config file', () => {
  const file = path.join(tmp, 'config.json');
  fs.writeFileSync(
    file,
    JSON.stringify({ repos: ['me/app'], agent: 'build', approvers: ['me'], proxy: 'socks5h://127.0.0.1:10808' })
  );
  const config = loadConfig(file);
  assert.equal(config.repos[0].fullName, 'me/app');
  assert.equal(config.repos[0].agent, 'build');
  assert.equal(config.pollIntervalSec, 30);
  assert.equal(config.webhookPort, 8788);
});

test('loadConfig rejects missing files', () => {
  assert.throws(() => loadConfig(path.join(tmp, 'missing.json')));
});
