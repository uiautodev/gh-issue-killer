import path from 'node:path';
import fs from 'node:fs';
import { HOME, TMP_DIR, run, whichSync, ensureDir, randomHex } from './util.mjs';
import { goEnv } from './proxy.mjs';

let GH_BIN = null;
let PROXY = null;

export function initGh(proxy) {
  GH_BIN = process.env.GH_AGENT_BIN || whichSync('gh-agent');
  if (!GH_BIN) {
    throw new Error('gh-agent not found in PATH (expected ~/.local/bin/gh-agent)');
  }
  PROXY = proxy;
  return GH_BIN;
}

export function ghBin() {
  return GH_BIN;
}

export function ghEnv(extra = {}) {
  const env = {
    ...process.env,
    ...goEnv(PROXY),
    GH_CONFIG_DIR: path.join(HOME, '.config', 'gh-agent'),
    GH_HOST: 'github.com',
    ...extra
  };
  delete env.GH_TOKEN;
  delete env.GITHUB_TOKEN;
  return env;
}

export async function runGh(args, options = {}) {
  if (!GH_BIN) throw new Error('initGh() was not called');
  return run(GH_BIN, args, {
    env: ghEnv(options.env),
    timeout: options.timeout ?? 180_000,
    allowFailure: options.allowFailure ?? false,
    cwd: options.cwd
  });
}

function tempBody(body) {
  ensureDir(TMP_DIR);
  const file = path.join(TMP_DIR, `body-${process.pid}-${Date.now()}-${randomHex(4)}.json`);
  fs.writeFileSync(file, JSON.stringify(body));
  return file;
}

export async function api(endpoint, options = {}) {
  const { method = 'GET', body, jq, paginate = false, allowFailure = false, timeout } = options;
  const args = ['api'];
  if (method !== 'GET') args.push('-X', method);
  let bodyFile = null;
  if (body !== undefined) {
    bodyFile = tempBody(body);
    args.push('--input', bodyFile);
  }
  if (jq) args.push('--jq', jq);
  if (paginate) args.push('--paginate');
  args.push(endpoint);

  try {
    const result = await runGh(args, { allowFailure: true, timeout });
    if (result.code !== 0) {
      const error = new Error(`gh api ${endpoint} failed (${result.code}): ${result.stderr.slice(-1500)}`);
      error.result = result;
      if (allowFailure) return { ok: false, status: result.code, error: result.stderr };
      throw error;
    }
    const stdout = result.stdout.trim();
    if (jq) return stdout;
    if (!stdout) return null;
    return JSON.parse(stdout);
  } finally {
    if (bodyFile) {
      try {
        fs.unlinkSync(bodyFile);
      } catch {
        // ignore
      }
    }
  }
}

export async function getToken() {
  const result = await runGh(['auth', 'token']);
  return result.stdout.trim();
}

export async function getAgentIdentity() {
  const user = await api('user');
  return {
    login: user.login,
    id: user.id,
    name: user.name || user.login,
    email: `${user.id}+${user.login}@users.noreply.github.com`
  };
}

export async function getRepoInfo(fullName) {
  const repo = await api(`repos/${fullName}`);
  return {
    id: repo.id,
    fullName: repo.full_name,
    owner: repo.owner.login,
    name: repo.name,
    defaultBranch: repo.default_branch || 'main',
    private: repo.private,
    permissions: repo.permissions || {}
  };
}

export async function getIssue(fullName, number) {
  return api(`repos/${fullName}/issues/${number}`);
}

export async function listOpenIssues(fullName, { since } = {}) {
  let endpoint = `repos/${fullName}/issues?state=open&per_page=100&sort=updated&direction=desc`;
  if (since) endpoint += `&since=${encodeURIComponent(since)}`;
  const issues = await api(endpoint);
  return Array.isArray(issues) ? issues.filter((issue) => !issue.pull_request) : [];
}

export async function listComments(fullName, number) {
  const comments = await api(
    `repos/${fullName}/issues/${number}/comments?per_page=100&sort=created&direction=asc`
  );
  return Array.isArray(comments) ? comments : [];
}

export async function postComment(fullName, number, body) {
  return api(`repos/${fullName}/issues/${number}/comments`, { method: 'POST', body: { body } });
}

export async function updateIssue(fullName, number, patch) {
  return api(`repos/${fullName}/issues/${number}`, { method: 'PATCH', body: patch });
}

export async function reactIssue(fullName, number, content) {
  return api(`repos/${fullName}/issues/${number}/reactions`, {
    method: 'POST',
    body: { content },
    allowFailure: true
  });
}

export async function reactComment(fullName, commentId, content) {
  return api(`repos/${fullName}/issues/comments/${commentId}/reactions`, {
    method: 'POST',
    body: { content },
    allowFailure: true
  });
}

export const PHASE_LABELS = [
  ['agent:planning', 'd4c5f0', 'opencode is analyzing the issue'],
  ['agent:awaiting-approval', 'fbca04', 'plan posted, waiting for lgtm/continue'],
  ['agent:in-progress', '1d76db', 'opencode is implementing'],
  ['agent:pr-open', '5319e7', 'PR opened, waiting for merge command'],
  ['agent:blocked', 'd93f0b', 'agent is blocked and needs input'],
  ['agent:done', '0e8a16', 'agent finished the task'],
  ['agent:cancelled', '6a737d', 'agent task cancelled']
];
export const PHASE_LABEL_NAMES = PHASE_LABELS.map(([name]) => name);

export async function ensureLabels(fullName) {
  for (const [name, color, description] of PHASE_LABELS) {
    const existing = await api(`repos/${fullName}/labels/${encodeURIComponent(name)}`, {
      allowFailure: true
    });
    if (existing && existing.ok === false) {
      await api(`repos/${fullName}/labels`, {
        method: 'POST',
        body: { name, color, description },
        allowFailure: true
      });
    }
  }
}

export async function setPhaseLabel(fullName, number, phaseLabel) {
  for (const name of PHASE_LABEL_NAMES) {
    if (name === phaseLabel) continue;
    await api(`repos/${fullName}/issues/${number}/labels/${encodeURIComponent(name)}`, {
      method: 'DELETE',
      allowFailure: true
    });
  }
  await api(`repos/${fullName}/issues/${number}/labels`, {
    method: 'POST',
    body: { labels: [phaseLabel] },
    allowFailure: true
  });
}

export async function createPr(fullName, { title, head, base, body }) {
  try {
    return await api(`repos/${fullName}/pulls`, { method: 'POST', body: { title, head, base, body } });
  } catch (error) {
    if (/A pull request already exists/i.test(error.message)) {
      const owner = fullName.split('/')[0];
      const list = await api(
        `repos/${fullName}/pulls?state=open&head=${encodeURIComponent(`${owner}:${head}`)}&per_page=1`
      );
      if (Array.isArray(list) && list.length > 0) {
        const updated = await api(`repos/${fullName}/pulls/${list[0].number}`, {
          method: 'PATCH',
          body: { title, body }
        });
        return updated;
      }
    }
    throw error;
  }
}

export async function getPr(fullName, number) {
  return api(`repos/${fullName}/pulls/${number}`);
}

export async function mergePr(fullName, number, { method = 'squash' } = {}) {
  return api(`repos/${fullName}/pulls/${number}/merge`, {
    method: 'PUT',
    body: { merge_method: method },
    allowFailure: true
  });
}

export async function deleteBranch(fullName, branch) {
  return api(`repos/${fullName}/git/refs/heads/${branch}`, { method: 'DELETE', allowFailure: true });
}

export async function listCheckRuns(fullName, sha) {
  return api(`repos/${fullName}/commits/${sha}/check-runs`, { allowFailure: true });
}

export async function listHooks(fullName) {
  return api(`repos/${fullName}/hooks?per_page=100`, { allowFailure: true });
}

export async function deleteHook(fullName, id) {
  return api(`repos/${fullName}/hooks/${id}`, { method: 'DELETE', allowFailure: true });
}

export async function canManageHooks(fullName) {
  const result = await listHooks(fullName);
  return Boolean(result && Array.isArray(result));
}
