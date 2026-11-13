import path from 'node:path';
import fs from 'node:fs';
import { loadConfig } from './config.mjs';
import { normalizeProxy, goEnv } from './proxy.mjs';
import * as gh from './gh.mjs';
import * as state from './state.mjs';
import { buildGitEnv, resolveRepo } from './git.mjs';
import { prepareShim } from './opencode.mjs';
import { createRouter } from './router.mjs';
import { startWebhookServer, startPoller, startWebhookForwarder } from './watcher.mjs';
import { ensureDirs, createLogger, STATE_DIR, randomHex } from './util.mjs';

const LOCK_FILE = path.join(STATE_DIR, 'daemon.lock');

function acquireLock(log) {
  if (fs.existsSync(LOCK_FILE)) {
    const pid = Number(fs.readFileSync(LOCK_FILE, 'utf8').trim());
    if (Number.isFinite(pid)) {
      try {
        process.kill(pid, 0);
        log.error(`another daemon is already running (pid ${pid}), exiting`);
        process.exit(1);
      } catch {
        log.warn(`removing stale lock (pid ${pid})`);
      }
    }
    fs.unlinkSync(LOCK_FILE);
  }
  fs.writeFileSync(LOCK_FILE, String(process.pid));
}

async function main() {
  ensureDirs();
  const config = loadConfig();
  const log = createLogger();
  const proxy = normalizeProxy(config.proxy);
  gh.initGh(proxy);
  state.load();
  acquireLock(log);

  log.info(`gh-issue-killer starting (config ${config.file}, proxy ${proxy ? proxy.raw : 'direct'})`);

  const identity = await gh.getAgentIdentity();
  const token = await gh.getToken();
  const shimDir = prepareShim({ ghAgentBin: gh.ghBin() });
  log.info(`agent identity: @${identity.login} (${identity.email})`);

  const contexts = new Map();
  for (const repo of config.repos) {
    const repoInfo = await gh.getRepoInfo(repo.fullName);
    if (!repoInfo.permissions?.push) {
      log.warn(`${repo.fullName}: gh-agent has no push access; implementation and merges will fail`);
    }
    const local = await resolveRepo({ repo, repoInfo, identity, token, proxy });
    const childEnv = {
      ...process.env,
      ...goEnv(proxy),
      ...buildGitEnv({ proxy, token, identity }),
      PATH: `${shimDir}:${process.env.PATH || ''}`,
      GWIK_AGENT: identity.login
    };
    delete childEnv.GH_TOKEN;
    delete childEnv.GITHUB_TOKEN;

    contexts.set(repo.fullName, {
      config,
      repo,
      repoInfo,
      local,
      identity,
      token,
      proxy,
      agentLogin: identity.login,
      log,
      childEnv
    });

    await gh.ensureLabels(repo.fullName);
    log.info(
      `repo ready: ${repo.fullName} clone=${local.dir} base=${local.base} worktrees=${local.worktreeRoot} approvers=${repo.approvers.join(',')}`
    );
  }

  const router = createRouter({ config, contexts, agentLogin: identity.login, log });

  const secret = state.getGlobal('webhookSecret') || randomHex(24);
  state.setGlobal('webhookSecret', secret);

  const server = config.webhook
    ? await startWebhookServer({ config, router, contexts, secret, log })
    : null;
  const poller = startPoller({ config, contexts, router, log });
  const forwarder = await startWebhookForwarder({ config, contexts, secret, log });

  await router.startupScan();
  poller.tick();

  log.info('daemon ready');

  let shuttingDown = false;
  const shutdown = async (signal) => {
    if (shuttingDown) return;
    shuttingDown = true;
    log.info(`shutting down (${signal})`);
    try {
      if (forwarder) await forwarder.stop();
      poller.stop();
      if (server) server.close();
      state.save();
    } finally {
      try {
        fs.unlinkSync(LOCK_FILE);
      } catch {
        // ignore
      }
      process.exit(0);
    }
  };
  process.on('SIGINT', () => shutdown('SIGINT'));
  process.on('SIGTERM', () => shutdown('SIGTERM'));
}

main().catch((error) => {
  console.error(error);
  process.exit(1);
});
