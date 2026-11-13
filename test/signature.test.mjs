import test from 'node:test';
import assert from 'node:assert/strict';
import crypto from 'node:crypto';
import { verifySignature } from '../src/watcher.mjs';

test('verifySignature accepts a valid HMAC and rejects bad ones', () => {
  const secret = 's3cret';
  const body = Buffer.from('{"hello":"world"}');
  const signature = `sha256=${crypto.createHmac('sha256', secret).update(body).digest('hex')}`;
  assert.equal(verifySignature(secret, body, signature), true);
  assert.equal(verifySignature(secret, body, 'sha256=deadbeef'), false);
  assert.equal(verifySignature(secret, body, undefined), false);
  assert.equal(verifySignature(null, body, undefined), true);
});
