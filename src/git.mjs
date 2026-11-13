import fs from 'node:fs';
import path from 'node:path';
import { STATE_DIR, run, ensureDir } from './util.mjs';
import { gitProxyEntries } from './proxy.mjs';

const REPOS_ROOT = path.join(STATE_DIR, 'repos');
const WORKTREES_ROOT = path.join(STATE_DIR, 'worktrees');

async function git(args, cwd, env, options = {}) {
  return run('git', args, {
    cwd,
    env,
    timeout: options.timeout ?? 300_000,
    allowFailure: options.allowFailure ?? false
  });
}

export function buildGitEnv({ proxy, token, identity }) {
  // Inherit the full environment: without PATH the child falls back to
  // /usr/bin/git (Apple's old git), whose `fetch --prune <url> <refspec>`
  // wrongly deletes the remote-tracking ref it just fetched.
  const env = { ...process.env };
  const entries = [];
  if (token) {
    const basic = Buffer.from(`x-access-token:${token}`).toString('base64');
    entries.push(['http.https://github.com/.extraheader', `AUTHORIZATION: basic ${basic}`]);
  }
  entries.push(...gitProxyEntries(proxy));
  if (entries.length > 0) {
    env.GIT_CONFIG_COUNT = String(entries.length);
    entries.forEach(([key, value], index) => {
      env[`GIT_CONFIG_KEY_${index}`] = key;
      env[`GIT_CONFIG_VALUE_${index}`] = value;
    });
  }
  env.GIT_TERMINAL_PROMPT = '0';
  if (identity) {
    env.GIT_AUTHOR_NAME = identity.name;
    env.GIT_AUTHOR_EMAIL = identity.email;
    env.GIT_COMMITTER_NAME = identity.name;
    env.GIT_COMMITTER_EMAIL = identity.email;
  }
  return env;
}

/**
 * Every watched repo lives in its own managed clone under
 * ~/.gh-issue-killer/repos/<owner>-<repo>. We never look at (or touch) the
 * user's working clones. If the managed clone already exists we just fetch the
 * base branch to the latest origin/<base>.
 */
export async function resolveRepo({ repo, repoInfo, identity, token, proxy }) {
  const env = buildGitEnv({ proxy, token, identity });
  const httpsUrl = `https://github.com/${repo.fullName}.git`;
  const base = repoInfo.defaultBranch;

  const cloneDir = path.join(REPOS_ROOT, `${repo.owner}-${repo.name}`);
  ensureDir(REPOS_ROOT);

  if (!fs.existsSync(path.join(cloneDir, '.git'))) {
    if (fs.existsSync(cloneDir) && fs.readdirSync(cloneDir).length > 0) {
      throw new Error(`managed clone path exists but is not a git repository: ${cloneDir}`);
    }
    await git(['clone', httpsUrl, cloneDir], path.dirname(cloneDir), env, { timeout: 900_000 });
  } else {
    await git(['remote', 'set-url', 'origin', httpsUrl], cloneDir, env, { allowFailure: true });
  }

  const worktreeRoot =
    repo.worktreeRoot || path.join(WORKTREES_ROOT, `${repo.owner}-${repo.name}`);
  ensureDir(worktreeRoot);

  return { dir: cloneDir, httpsUrl, base, worktreeRoot, env };
}

export async function fetchBase(local, branchRef) {
  // The leading '+' forces the ref update: fetching from a URL does not treat
  // refs/remotes/... as tracking refs, and non-forced updates fail to lock.
  // No --prune: it is unnecessary here and misbehaves on URL fetches.
  await git(
    ['fetch', local.httpsUrl, `+${branchRef}:refs/remotes/origin/${branchRef}`],
    local.dir,
    local.env,
    { timeout: 300_000 }
  );
}

export async function worktreeList(local) {
  const result = await git(['worktree', 'list', '--porcelain'], local.dir, local.env, { allowFailure: true });
  return result.stdout.trim();
}

export async function createWorktree(local, { issueNumber, key }) {
  const name = `issue-${issueNumber}-${key}`;
  const branch = `agent/issue-${issueNumber}-${key}`;
  const dir = path.join(local.worktreeRoot, name);
  ensureDir(local.worktreeRoot);

  if (fs.existsSync(path.join(dir, '.git'))) {
    const current = await git(['branch', '--show-current'], dir, local.env, { allowFailure: true });
    if (current.stdout.trim() === branch) return { dir, branch, reused: true };
    throw new Error(`worktree ${dir} exists on a different branch (${current.stdout.trim()})`);
  }

  const existing = await git(['rev-parse', '--verify', `refs/heads/${branch}`], local.dir, local.env, {
    allowFailure: true
  });
  if (existing.code === 0) {
    await git(['worktree', 'add', dir, branch], local.dir, local.env);
  } else {
    await git(['worktree', 'add', '-b', branch, dir, `origin/${local.base}`], local.dir, local.env);
  }
  return { dir, branch, reused: false };
}

export async function createDetachedWorktree(local, suffix) {
  const dir = path.join(STATE_DIR, 'tmp', `plan-${suffix}-${Date.now()}`);
  ensureDir(path.dirname(dir));
  await git(['worktree', 'add', '--detach', dir, `origin/${local.base}`], local.dir, local.env);
  return dir;
}

export async function removeWorktree(local, dir, { force = false } = {}) {
  await git(['worktree', 'remove', ...(force ? ['--force'] : []), dir], local.dir, local.env, {
    allowFailure: true
  });
  await git(['worktree', 'prune'], local.dir, local.env, { allowFailure: true });
}

export async function status(dir, env) {
  const result = await git(['status', '--porcelain'], dir, env, { allowFailure: true });
  return result.stdout.trim();
}

export async function commitAll(dir, env, message) {
  const dirty = await status(dir, env);
  if (!dirty) return { committed: false };
  await git(['add', '-A'], dir, env);
  await git(['commit', '-m', message], dir, env);
  return { committed: true };
}

export async function logSinceBase(dir, local) {
  const result = await git(['log', '--oneline', `origin/${local.base}..HEAD`], dir, local.env, {
    allowFailure: true
  });
  return result.stdout.trim();
}

export async function diffStat(dir, local) {
  const result = await git(['diff', '--stat', `origin/${local.base}...HEAD`], dir, local.env, {
    allowFailure: true
  });
  return result.stdout.trim();
}

export async function changedFiles(dir, local) {
  const result = await git(['diff', '--name-only', `origin/${local.base}...HEAD`], dir, local.env, {
    allowFailure: true
  });
  return result.stdout.trim().split('\n').filter(Boolean);
}

export async function headSha(dir, env) {
  const result = await git(['rev-parse', 'HEAD'], dir, env);
  return result.stdout.trim();
}

export async function pushBranch(dir, local, branch) {
  await git(['push', local.httpsUrl, `${branch}:${branch}`], dir, local.env, { timeout: 300_000 });
}

export async function deleteLocalBranch(local, branch) {
  await git(['branch', '-D', branch], local.dir, local.env, { allowFailure: true });
}
