import http from 'node:http';
import crypto from 'node:crypto';
import { spawn } from 'node:child_process';
import * as gh from './gh.mjs';
import * as state from './state.mjs';
import { goEnv } from './proxy.mjs';
import { sleep } from './util.mjs';

const MAX_BODY = 10 * 1024 * 1024;

export function verifySignature(secret, body, header) {
  if (!secret) return true;
  if (!header || !header.startsWith('sha256=')) return false;
  const expected = `sha256=${crypto.createHmac('sha256', secret).update(body).digest('hex')}`;
  const a = Buffer.from(expected);
  const b = Buffer.from(header);
  return a.length === b.length && crypto.timingSafeEqual(a, b);
}

export function startWebhookServer({ config, router, contexts, secret, log }) {
  const server = http.createServer((request, response) => {
    if (request.method === 'GET' && request.url === '/health') {
      response.writeHead(200).end('ok');
      return;
    }
    if (request.method !== 'POST' || !request.url.startsWith('/gh')) {
      response.writeHead(404).end('not found');
      return;
    }

    const chunks = [];
    let size = 0;
    request.on('data', (chunk) => {
      size += chunk.length;
      if (size > MAX_BODY) {
        request.destroy();
        return;
      }
      chunks.push(chunk);
    });
    request.on('error', () => undefined);
    request.on('end', () => {
      const body = Buffer.concat(chunks);
      response.writeHead(202).end('accepted');

      const event = request.headers['x-github-event'];
      const delivery = request.headers['x-github-delivery'];
      const signature = request.headers['x-hub-signature-256'];
      if (!verifySignature(secret, body, signature)) {
        log.warn(`webhook signature mismatch (delivery ${delivery}), ignoring`);
        return;
      }

      let payload = null;
      try {
        payload = JSON.parse(body.toString('utf8'));
      } catch {
        return;
      }
      if (!payload || !payload.repository?.full_name || !contexts.has(payload.repository.full_name)) return;

      const fullName = payload.repository.full_name;
      const issue = payload.issue;

      if (event === 'issues' && issue && ['opened', 'assigned', 'edited', 'reopened'].includes(payload.action)) {
        router
          .handleTrigger(fullName, {
            type: 'issue',
            issueNumber: issue.number,
            sender: payload.sender?.login,
            key: `hook-${delivery}`,
            deliveryId: delivery
          })
          .catch((error) => log.error(`webhook handling failed: ${error.message}`));
        return;
      }

      if (event === 'issue_comment' && payload.action === 'created' && issue && payload.comment) {
        router
          .handleTrigger(fullName, {
            type: 'comment',
            issueNumber: issue.number,
            commentId: payload.comment.id,
            commentBody: payload.comment.body,
            sender: payload.sender?.login,
            key: `hook-${delivery}`,
            deliveryId: delivery
          })
          .catch((error) => log.error(`webhook handling failed: ${error.message}`));
      }
    });
  });

  return new Promise((resolve) => {
    server.listen(config.webhookPort, config.webhookHost, () => {
      log.info(`webhook receiver on http://${config.webhookHost}:${config.webhookPort}/gh`);
      resolve(server);
    });
  });
}

export function startPoller({ config, contexts, router, log }) {
  let timer = null;
  let running = false;

  async function tick() {
    if (running) return;
    running = true;
    try {
      for (const fullName of contexts.keys()) {
        await router.pollOnce(fullName);
      }
    } catch (error) {
      log.warn(`poll failed: ${error.message}`);
    } finally {
      running = false;
    }
  }

  timer = setInterval(tick, config.pollIntervalSec * 1000);
  timer.unref?.();
  log.info(`poller started, interval ${config.pollIntervalSec}s`);
  return {
    stop() {
      if (timer) clearInterval(timer);
    },
    tick
  };
}

async function cleanupCliHooks(fullName, log) {
  const hooks = await gh.listHooks(fullName);
  if (!Array.isArray(hooks)) return;
  for (const hook of hooks) {
    if (hook.name === 'cli' && /webhook-forward|ghcli/i.test(hook.config?.url || '')) {
      await gh.deleteHook(fullName, hook.id);
      log.info(`removed stale forward hook ${hook.id}`);
    }
  }
}

export async function startWebhookForwarder({ config, contexts, secret, log }) {
  if (!config.webhook) {
    log.info('webhook disabled by config, using polling only');
    return null;
  }

  const repos = [...contexts.keys()];
  let forwardable = true;
  for (const fullName of repos) {
    const info = await gh.getRepoInfo(fullName);
    if (!info.permissions?.admin) {
      log.warn(
        `${fullName}: gh-agent is not admin; webhook creation skipped, polling only. ` +
          'Grant admin on the repo and refresh the token (GH_CONFIG_DIR=~/.config/gh-agent gh auth refresh -h github.com -s admin:repo_hook) for real-time events.'
      );
      forwardable = false;
    }
    const hooks = await gh.listHooks(fullName);
    if (!Array.isArray(hooks)) {
      log.warn(
        `${fullName}: gh-agent token cannot manage webhooks (needs admin:repo_hook scope); polling only.`
      );
      forwardable = false;
    }
  }
  if (!forwardable) return null;

  for (const fullName of repos) await cleanupCliHooks(fullName, log);

  const installed = await gh.runGh(['extension', 'list'], { allowFailure: true });
  if (!/gh-webhook|cli\/gh-webhook/.test(installed.stdout || '')) {
    log.info('installing cli/gh-webhook extension');
    await gh.runGh(['extension', 'install', 'cli/gh-webhook'], { allowFailure: true, timeout: 300_000 });
  }

  let stopping = false;
  const children = new Map();

  function spawnForwarder(fullName) {
    if (stopping) return;
    const args = [
      'webhook',
      'forward',
      '--repo',
      fullName,
      '--events=issues,issue_comment',
      `--url=http://${config.webhookHost}:${config.webhookPort}/gh`,
      `--secret=${secret}`
    ];
    log.info(`starting gh webhook forward for ${fullName}`);
    const child = spawn(gh.ghBin(), args, {
      env: { ...process.env, ...goEnv(contexts.get(fullName).proxy) },
      stdio: ['ignore', 'pipe', 'pipe']
    });
    children.set(fullName, child);
    child.stderr.on('data', (chunk) => {
      const text = chunk.toString().trim();
      if (text) log.info(`[forward ${fullName}] ${text}`);
    });
    child.on('exit', async (code) => {
      children.delete(fullName);
      if (stopping) return;
      log.warn(`webhook forwarder for ${fullName} exited (${code}), restarting in 5s`);
      await sleep(5_000);
      spawnForwarder(fullName);
    });
  }

  for (const fullName of repos) spawnForwarder(fullName);

  return {
    async stop() {
      stopping = true;
      for (const child of children.values()) child.kill('SIGTERM');
      for (const fullName of repos) await cleanupCliHooks(fullName, log);
    }
  };
}

