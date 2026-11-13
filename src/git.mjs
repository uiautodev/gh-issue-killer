import fs from 'node:fs';
import path from 'node:path';
import { HOME, STATE_DIR, run, ensureDir } from './util.mjs';
import { gitProxyEntries } from './proxy.mjs';

const SEARCH_ROOTS = [
  path.join(HOME, 'src'),
  path.join(HOME, 'Sync', 'Projects'),
  path.join(HOME, 'Projects'),
  path.join(HOME, 'Developer'),
  path.join(HOME, 'code')
];

const FALLBACK_ROOT = path.join(STATE_DIR, 'repos');

function normalizeRemote(url) {
  if (!url) return '';
  return url
    .trim()
    .toLowerCase()
    .replace(/^git@github\.com:/, '')
    .replace(/^ssh:\/\/git@ssh\.github\.com:443\//, '')
    .replace(/^ssh:\/\/git@github\.com(:22)?\//, '')
    .replace(/^https:\/\/[^@]*@?github\.com\//, '')
    .replace(/\.git$/, '');
}

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

function candidateDirs() {
  const dirs = [];
  for (const root of SEARCH_ROOTS) {
    if (!fs.existsSync(root)) continue;
    let children = [];
    try {
      children = fs.readdirSync(root, { withFileTypes: true }).filter((entry) => entry.isDirectory());
    } catch {
      continue;
    }
    for (const child of children) {
      const dir = path.join(root, child.name);
      if (child.name.startsWith('.') || child.name.endsWith('-work')) continue;
      if (fs.existsSync(path.join(dir, '.git'))) {
        dirs.push(dir);
        continue;
      }
      // one level deeper, e.g. ~/Sync/Projects/<org>/<repo>
      try {
        const nested = fs
          .readdirSync(dir, { withFileTypes: true })
          .filter((entry) => entry.isDirectory());
        for (const item of nested) {
          const sub = path.join(dir, item.name);
          if (item.name.startsWith('.') || item.name.endsWith('-work')) continue;
          if (fs.existsSync(path.join(sub, '.git'))) dirs.push(sub);
        }
      } catch {
        // ignore
      }
    }
  }
  return dirs;
}

async function probeClone(dir, fullName, env) {
  try {
    const remote = await git(['remote', 'get-url', 'origin'], dir, env, { allowFailure: true, timeout: 15_000 });
    if (remote.code !== 0) return null;
    if (normalizeRemote(remote.stdout) !== fullName.toLowerCase()) return null;
    const status = await git(['status', '--porcelain'], dir, env, { allowFailure: true, timeout: 30_000 });
    return { dir, clean: status.code === 0 && status.stdout.trim() === '' };
  } catch {
    return null;
  }
}

export async function resolveRepo({ repo, repoInfo, identity, token, proxy }) {
  const env = buildGitEnv({ proxy, token, identity });
  const httpsUrl = `https://github.com/${repo.fullName}.git`;
  const base = repoInfo.defaultBranch;

  let cloneDir = null;
  if (repo.clone) {
    if (!fs.existsSync(path.join(repo.clone, '.git'))) {
      throw new Error(`configured clone is not a git repository: ${repo.clone}`);
    }
    const probed = await probeClone(repo.clone, repo.fullName, env);
    if (!probed) throw new Error(`configured clone remote does not match ${repo.fullName}`);
    cloneDir = repo.clone;
  } else {
    const candidates = candidateDirs();
    const probed = await Promise.all(candidates.map((dir) => probeClone(dir, repo.fullName, env)));
    const matches = probed.filter(Boolean);
    // Prefer a clone with a sibling -work directory (the convention this setup
    // already uses), then a clean one.
    matches.sort((a, b) => {
      const aWork = fs.existsSync(`${a.dir}-work`) ? 0 : 1;
      const bWork = fs.existsSync(`${b.dir}-work`) ? 0 : 1;
      if (aWork !== bWork) return aWork - bWork;
      if (a.clean !== b.clean) return a.clean ? -1 : 1;
      return 0;
    });
    cloneDir = matches[0]?.dir || null;
  }

  let cloned = false;
  if (!cloneDir) {
    cloneDir = path.join(FALLBACK_ROOT, `${repo.owner}-${repo.name}`);
    if (!fs.existsSync(path.join(cloneDir, '.git'))) {
      ensureDir(path.dirname(cloneDir));
      await git(['clone', httpsUrl, cloneDir], path.dirname(cloneDir), env, { timeout: 600_000 });
      cloned = true;
    }
  }

  const worktreeRoot =
    repo.worktreeRoot ||
    (cloned ? path.join(STATE_DIR, 'worktrees', `${repo.owner}-${repo.name}`) : `${cloneDir}-work`);

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
