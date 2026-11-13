import test from 'node:test';
import assert from 'node:assert/strict';
import { normalizeProxy, goEnv, gitProxyEntries, curlProxyArgs } from '../src/proxy.mjs';

test('normalizeProxy parses socks5h and fixes the single-slash typo', () => {
  const proxy = normalizeProxy('socks5h://127.0.0.1:10808');
  assert.equal(proxy.protocol, 'socks5h');
  assert.equal(proxy.host, '127.0.0.1');
  assert.equal(proxy.port, '10808');

  const typo = normalizeProxy('socks5h:/x127.0.0.1:10808');
  assert.equal(typo.raw, 'socks5h://127.0.0.1:10808');
  assert.equal(typo.protocol, 'socks5h');
});

test('normalizeProxy rejects garbage', () => {
  assert.throws(() => normalizeProxy('ftp://127.0.0.1:21'));
  assert.throws(() => normalizeProxy('socks5://noport'));
  assert.equal(normalizeProxy(null), null);
});

test('goEnv downgrades socks5h to socks5 for Go clients', () => {
  const env = goEnv(normalizeProxy('socks5h://127.0.0.1:10808'));
  assert.equal(env.HTTPS_PROXY, 'socks5://127.0.0.1:10808');
  assert.equal(env.NO_PROXY, '127.0.0.1,localhost,::1');
});

test('git keeps socks5h for remote DNS', () => {
  const entries = gitProxyEntries(normalizeProxy('socks5h://127.0.0.1:10808'));
  assert.deepEqual(entries, [
    ['http.proxy', 'socks5h://127.0.0.1:10808'],
    ['https.proxy', 'socks5h://127.0.0.1:10808']
  ]);
  assert.deepEqual(gitProxyEntries(null), []);
});

test('curlProxyArgs uses socks5-hostname for remote DNS', () => {
  assert.deepEqual(curlProxyArgs(normalizeProxy('socks5h://127.0.0.1:10808')), [
    '--socks5-hostname',
    '127.0.0.1:10808'
  ]);
  assert.deepEqual(curlProxyArgs(null), []);
});
