import test from 'node:test';
import assert from 'node:assert/strict';
import { parseCommand, isMention, isRelevantIssue } from '../src/router.mjs';

test('isMention matches case-insensitively with word boundary', () => {
  assert.equal(isMention('hey @My-Agent look', 'my-agent'), true);
  assert.equal(isMention('hey @my-agent-2 look', 'my-agent'), false);
  assert.equal(isMention('no mention here', 'my-agent'), false);
});

test('parseCommand recognises commands in short comments', () => {
  assert.equal(parseCommand('lgtm'), 'approve');
  assert.equal(parseCommand('  continue  '), 'approve');
  assert.equal(parseCommand('@my-agent lgtm'), 'approve');
  assert.equal(parseCommand('合并'), 'merge');
  assert.equal(parseCommand('merge it'), 'merge');
  assert.equal(parseCommand('ship'), 'merge');
  assert.equal(parseCommand('cancel'), 'cancel');
  assert.equal(parseCommand('停止'), 'cancel');
  assert.equal(parseCommand('please continue with the remaining work'), 'approve');
});

test('parseCommand ignores long feedback and random prose', () => {
  assert.equal(parseCommand('continuously improving'), null);
  assert.equal(parseCommand('x'.repeat(250)), null);
  assert.equal(parseCommand('looks reasonable overall, a few notes below...'), null);
});

test('isRelevantIssue needs assignment or mention and skips PRs and self-authored issues', () => {
  const base = { number: 1, user: { login: 'human' }, assignees: [], labels: [] };
  assert.equal(isRelevantIssue({ ...base, assignees: [{ login: 'my-agent' }] }, 'my-agent'), true);
  assert.equal(isRelevantIssue({ ...base, body: 'cc @my-agent' }, 'my-agent'), true);
  assert.equal(isRelevantIssue({ ...base, pull_request: {} }, 'my-agent'), false);
  assert.equal(isRelevantIssue({ ...base, user: { login: 'my-agent' } }, 'my-agent'), false);
  assert.equal(isRelevantIssue({ ...base }, 'my-agent'), false);
});
