import path from 'node:path';
import fs from 'node:fs';
import { STATE_DIR, readJsonSync, atomicWriteJson, nowIso } from './util.mjs';

const FILE = path.join(STATE_DIR, 'state.json');
const MAX_COMMENTS = 500;
const MAX_DELIVERIES = 1000;

let state = { version: 1, global: {}, repos: {} };

export function load() {
  const loaded = readJsonSync(FILE, null);
  if (loaded) {
    state = loaded;
    if (!state.global) state.global = {};
    if (!state.repos) state.repos = {};
  }
  return state;
}

export function save() {
  atomicWriteJson(FILE, state);
}

function repoState(fullName) {
  if (!state.repos[fullName]) state.repos[fullName] = { issues: {}, meta: {} };
  if (!state.repos[fullName].issues) state.repos[fullName].issues = {};
  if (!state.repos[fullName].meta) state.repos[fullName].meta = {};
  return state.repos[fullName];
}

export function getIssue(fullName, number) {
  return repoState(fullName).issues[String(number)] || null;
}

export function setIssue(fullName, number, patch) {
  const repo = repoState(fullName);
  const key = String(number);
  const current = repo.issues[key] || {
    phase: 'new',
    createdAt: nowIso(),
    processedComments: [],
    processedDeliveries: []
  };
  const next = {
    ...current,
    ...patch,
    processedComments: patch.processedComments ?? current.processedComments ?? [],
    processedDeliveries: patch.processedDeliveries ?? current.processedDeliveries ?? []
  };
  next.updatedAt = nowIso();
  repo.issues[key] = next;
  save();
  return next;
}

export function setRepoMeta(fullName, patch) {
  const repo = repoState(fullName);
  repo.meta = { ...repo.meta, ...patch };
  save();
}

export function getRepoMeta(fullName) {
  return repoState(fullName).meta;
}

export function isCommentProcessed(fullName, number, commentId) {
  const issue = getIssue(fullName, number);
  if (!issue) return false;
  return (issue.processedComments || []).includes(commentId);
}

export function markCommentProcessed(fullName, number, commentId) {
  const issue = getIssue(fullName, number);
  if (!issue) return;
  const list = issue.processedComments || [];
  if (!list.includes(commentId)) {
    list.push(commentId);
    if (list.length > MAX_COMMENTS) list.splice(0, list.length - MAX_COMMENTS);
  }
  setIssue(fullName, number, { processedComments: list, lastCommentId: commentId });
}

export function isDeliveryProcessed(fullName, deliveryId) {
  if (!deliveryId) return false;
  const repo = repoState(fullName);
  const list = repo.meta.processedDeliveries || [];
  return list.includes(deliveryId);
}

export function markDeliveryProcessed(fullName, deliveryId) {
  if (!deliveryId) return;
  const repo = repoState(fullName);
  const list = repo.meta.processedDeliveries || [];
  if (!list.includes(deliveryId)) {
    list.push(deliveryId);
    if (list.length > MAX_DELIVERIES) list.splice(0, list.length - MAX_DELIVERIES);
    setRepoMeta(fullName, { processedDeliveries: list });
  }
}

export function getGlobal(key, fallback = null) {
  return state.global[key] ?? fallback;
}

export function setGlobal(key, value) {
  state.global[key] = value;
  save();
}

export function filePath() {
  return FILE;
}

export function resetForTests(file = FILE) {
  if (fs.existsSync(file)) fs.unlinkSync(file);
}
