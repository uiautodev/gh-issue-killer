import assert from 'node:assert/strict';
import { test } from 'node:test';
import { addRepoNames, parseRepoNames, removeRepoName } from './repos.ts';

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
