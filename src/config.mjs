import fs from 'node:fs';
import path from 'node:path';
import { expandHome } from './util.mjs';

const DEFAULT_MODEL = process.env.GWIK_MODEL || 'magpie/group/deepseek';

export function normalizeRepos(rawRepos, rawDefaults = {}) {
  if (!rawRepos || (Array.isArray(rawRepos) && rawRepos.length === 0)) {
    throw new Error('config.repos is required');
  }

  const entries = [];
  if (Array.isArray(rawRepos)) {
    for (const item of rawRepos) {
      if (typeof item === 'string') entries.push([item, {}]);
      else entries.push([item.repo, item]);
    }
  } else if (typeof rawRepos === 'object') {
    for (const [name, options] of Object.entries(rawRepos)) entries.push([name, options || {}]);
  } else {
    throw new Error('config.repos must be an array or an object');
  }

  const seen = new Set();
  const repos = entries.map(([fullName, options]) => {
    const clean = String(fullName).trim().replace(/^https?:\/\/github\.com\//, '').replace(/\.git$/, '');
    if (!/^[A-Za-z0-9_.-]+\/[A-Za-z0-9_.-]+$/.test(clean)) {
      throw new Error(`invalid repo name: ${fullName}`);
    }
    if (seen.has(clean)) throw new Error(`duplicate repo in config: ${clean}`);
    seen.add(clean);
    const [owner, name] = clean.split('/');
    const approvers = options.approvers ?? rawDefaults.approvers ?? [];
    return {
      fullName: clean,
      owner,
      name,
      agent: options.agent ?? rawDefaults.agent ?? 'opencode',
      model: options.model ?? rawDefaults.model ?? DEFAULT_MODEL,
      approvers: Array.isArray(approvers) ? approvers : [approvers],
      clone: options.clone ? expandHome(options.clone) : null,
      worktreeRoot: options.worktreeRoot ? expandHome(options.worktreeRoot) : null
    };
  });
  return repos;
}

export function loadConfig(file = process.env.GWIK_CONFIG || path.join(process.cwd(), 'config.json')) {
  if (!fs.existsSync(file)) {
    throw new Error(`config not found: ${file} (copy config.example.json to config.json)`);
  }
  const raw = JSON.parse(fs.readFileSync(file, 'utf8'));

  const config = {
    file,
    proxy: raw.proxy || null,
    pollIntervalSec: Number(raw.pollIntervalSec ?? 30),
    webhook: raw.webhook !== false,
    webhookPort: Number(raw.webhookPort ?? 8788),
    webhookHost: raw.webhookHost || '127.0.0.1',
    worktreeRoot: raw.worktreeRoot ? expandHome(raw.worktreeRoot) : null,
    maxFixRounds: Number(raw.maxFixRounds ?? 3),
    maxParallel: Number(raw.maxParallel ?? 1),
    planTimeoutMin: Number(raw.planTimeoutMin ?? 15),
    implementTimeoutMin: Number(raw.implementTimeoutMin ?? 60),
    agent: raw.agent || 'opencode',
    model: raw.model || DEFAULT_MODEL,
    approvers: raw.approvers || [],
    repos: []
  };

  if (config.pollIntervalSec < 10) config.pollIntervalSec = 10;
  config.repos = normalizeRepos(raw.repos, {
    agent: config.agent,
    model: config.model,
    approvers: config.approvers
  });

  for (const repo of config.repos) {
    if (repo.approvers.length === 0) repo.approvers = [repo.owner];
  }

  return config;
}
