import fs from 'node:fs';
import path from 'node:path';
import { readJsonSync, runShell, ensureDir, tail } from './util.mjs';

const DEFAULT_TIMEOUT_SEC = 600;

function detectDefaultChecks(worktree) {
  const checks = [];
  const exists = (relative) => fs.existsSync(path.join(worktree, relative));
  const pkg = readJsonSync(path.join(worktree, 'package.json'), null);

  if (exists('go.mod')) {
    checks.push({ name: 'go test', cmd: 'go test ./...', cwd: '.', timeoutSec: 900 });
    checks.push({ name: 'go vet', cmd: 'go vet ./...', cwd: '.', timeoutSec: 300 });
  }
  if (exists('frontend/package.json')) {
    const frontendPkg = readJsonSync(path.join(worktree, 'frontend', 'package.json'), null);
    const scripts = frontendPkg?.scripts || {};
    checks.push({
      name: 'frontend install',
      cmd: 'npm install --no-audit --no-fund',
      cwd: 'frontend',
      timeoutSec: 900
    });
    if (scripts.check) {
      checks.push({ name: 'frontend check', cmd: 'npm run check', cwd: 'frontend', timeoutSec: 600 });
    }
    if (scripts.build) {
      checks.push({ name: 'frontend build', cmd: 'npm run build', cwd: 'frontend', timeoutSec: 900 });
    }
  } else if (pkg && pkg.scripts && pkg.scripts.test) {
    checks.push({ name: 'npm test', cmd: 'npm test', cwd: '.', timeoutSec: 900 });
  }
  return checks;
}

export function loadChecks({ worktree, runDir, log }) {
  const manifest = readJsonSync(path.join(runDir, 'checks.json'), null);
  let checks = null;
  if (manifest && Array.isArray(manifest.checks) && manifest.checks.length > 0) {
    checks = manifest.checks.map((check) => ({
      name: check.name || check.cmd,
      cmd: check.cmd,
      cwd: check.cwd || '.',
      timeoutSec: Number(check.timeoutSec || DEFAULT_TIMEOUT_SEC)
    }));
  } else {
    checks = detectDefaultChecks(worktree);
    if (checks.length === 0) {
      checks = [{ name: 'no checks detected', cmd: 'true', cwd: '.', timeoutSec: 30 }];
    }
    log?.warn?.('checks.json missing, using detected defaults', checks.map((c) => c.cmd));
  }
  return checks;
}

export async function runChecks({ checks, worktree, runDir, env, log }) {
  const logDir = path.join(runDir, 'checks');
  ensureDir(logDir);
  const results = [];

  for (const check of checks) {
    const cwd = path.resolve(worktree, check.cwd || '.');
    log.info(`check start: ${check.name} (${check.cmd})`);
    const started = Date.now();
    const result = await runShell(check.cmd, {
      cwd,
      env,
      timeout: check.timeoutSec * 1000,
      allowFailure: true
    });
    const durationSec = Math.round((Date.now() - started) / 1000);
    const ok = result.code === 0;
    try {
      fs.writeFileSync(
        path.join(logDir, `${check.name.replace(/[^a-zA-Z0-9_.-]+/g, '_')}.log`),
        `$ ${check.cmd}\ncwd: ${check.cwd}\nexit: ${result.code}\n\n${result.stdout}\n${result.stderr}\n`
      );
    } catch {
      // ignore
    }
    log.info(`check ${ok ? 'pass' : 'FAIL'}: ${check.name} (${durationSec}s)`);
    results.push({
      name: check.name,
      cmd: check.cmd,
      cwd: check.cwd,
      ok,
      code: result.code,
      timedOut: result.timedOut,
      durationSec,
      outputTail: tail(`${result.stdout}\n${result.stderr}`, 60)
    });
  }

  return { results, ok: results.every((item) => item.ok) };
}

export function formatChecksForComment(results) {
  return results
    .map((item) => `- ${item.ok ? '✅' : '❌'} \`${item.cmd}\`${item.cwd && item.cwd !== '.' ? ` (${item.cwd})` : ''}${item.timedOut ? ' [timeout]' : ''}`)
    .join('\n');
}
