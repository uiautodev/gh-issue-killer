import assert from 'node:assert/strict';
import { test } from 'node:test';
import {
	addRepoNames,
	parseRepoNames,
	repoCodingAgent,
	repoString,
	removeRepoName,
	setRepoStrings
} from './repos.ts';

test('parseRepoNames normalises URLs, .git suffixes and separators', () => {
	assert.deepEqual(parseRepoNames('https://github.com/a/b.git, c/d; e/f\n'), [
		'a/b',
		'c/d',
		'e/f'
	]);
});

test('addRepoNames keeps existing order and drops duplicates', () => {
	const text = 'a/b\nc/d';
	assert.equal(addRepoNames(text, 'c/d, e/f\na/b'), 'a/b\nc/d\ne/f');
	assert.equal(addRepoNames(text, 'https://github.com/c/d.git'), text);
});

test('addRepoNames handles an empty list', () => {
	assert.equal(addRepoNames('', 'owner/repo'), 'owner/repo');
	assert.equal(addRepoNames('', '   '), '');
});

test('removeRepoName removes only the requested repo', () => {
	assert.equal(removeRepoName('a/b\nc/d\ne/f', 'c/d'), 'a/b\ne/f');
	assert.equal(removeRepoName('a/b', 'a/b'), '');
	assert.equal(removeRepoName('a/b', 'x/y'), 'a/b');
});

test('repoCodingAgent reads the override of every repos shape', () => {
	assert.equal(repoCodingAgent(['a/b'], 'a/b'), '');
	assert.equal(repoCodingAgent([{ repo: 'a/b', codingAgent: ' claude ' }], 'a/b'), 'claude');
	assert.equal(repoCodingAgent({ 'a/b': { codingAgent: 'codex' } }, 'a/b'), 'codex');
	assert.equal(repoCodingAgent({ 'a/b': {} }, 'a/b'), '');
	assert.equal(repoCodingAgent(undefined, 'a/b'), '');
});

test('repoString reads any per-repo key and trims it', () => {
	assert.equal(repoString({ 'a/b': { model: ' sonnet ' } }, 'a/b', 'model'), 'sonnet');
	assert.equal(repoString([{ repo: 'a/b', language: 'zh' }], 'a/b', 'language'), 'zh');
	assert.equal(repoString(['a/b'], 'a/b', 'model'), '');
	assert.equal(repoString({ 'a/b': { model: 3 } }, 'a/b', 'model'), '');
	assert.equal(repoString(undefined, 'a/b', 'model'), '');
});

test('setRepoStrings writes one field per repo and keeps the others', () => {
	const doc: Record<string, unknown> = {
		repos: { 'a/b': { model: 'sonnet', language: 'en' }, 'c/d': {} }
	};
	setRepoStrings(doc, ['a/b', 'c/d'], 'codingAgent', { 'a/b': 'claude', 'c/d': 'codex' });
	const repos = doc.repos as Record<string, Record<string, unknown>>;
	assert.deepEqual(repos['a/b'], { model: 'sonnet', language: 'en', codingAgent: 'claude' });
	assert.equal(repos['c/d'].codingAgent, 'codex');

	setRepoStrings(doc, ['a/b', 'c/d'], 'model', { 'a/b': 'opus', 'c/d': '' });
	assert.deepEqual(repos['a/b'], {
		model: 'opus',
		language: 'en',
		codingAgent: 'claude'
	});
	assert.equal('model' in repos['c/d'], false);
});

test('setRepoStrings deletes the key for "follow global"', () => {
	const doc: Record<string, unknown> = {
		repos: [{ repo: 'a/b', codingAgent: 'claude', release: { enabled: true } }, { repo: 'c/d' }]
	};
	setRepoStrings(doc, ['a/b', 'c/d'], 'codingAgent', { 'a/b': '', 'c/d': 'codex' });
	const repos = doc.repos as Record<string, Record<string, unknown>>[];
	assert.deepEqual(repos[0], { repo: 'a/b', release: { enabled: true } });
	assert.equal(repos[1].codingAgent, 'codex');
});

test('setRepoStrings leaves a plain name list alone without overrides', () => {
	const doc: Record<string, unknown> = { repos: ['a/b', 'c/d'] };
	setRepoStrings(doc, ['a/b', 'c/d'], 'codingAgent', { 'a/b': '', 'c/d': '' });
	assert.deepEqual(doc.repos, ['a/b', 'c/d']);
});

test('setRepoStrings grows a name list into objects for one override', () => {
	const doc: Record<string, unknown> = { repos: ['a/b', 'c/d'] };
	setRepoStrings(doc, ['a/b', 'c/d'], 'codingAgent', { 'a/b': 'claude', 'c/d': '' });
	assert.deepEqual(doc.repos, [{ repo: 'a/b', codingAgent: 'claude' }, { repo: 'c/d' }]);
});
