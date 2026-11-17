import assert from 'node:assert/strict';
import { test } from 'node:test';
import { formatStars } from './utils.ts';

test('formatStars keeps counts below 1000 exact', () => {
	assert.equal(formatStars(0), '0');
	assert.equal(formatStars(7), '7');
	assert.equal(formatStars(999), '999');
});

test('formatStars compacts thousands to one decimal', () => {
	assert.equal(formatStars(1000), '1k');
	assert.equal(formatStars(1234), '1.2k');
	assert.equal(formatStars(9300), '9.3k');
});

test('formatStars rounds when the compact form no longer needs a decimal', () => {
	assert.equal(formatStars(9999), '10k');
	assert.equal(formatStars(12_345), '12.3k');
	assert.equal(formatStars(123_456), '123k');
});

test('formatStars rejects unusable values', () => {
	assert.equal(formatStars(-1), '0');
	assert.equal(formatStars(Number.NaN), '0');
	assert.equal(formatStars(Number.POSITIVE_INFINITY), '0');
});
