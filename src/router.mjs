import * as gh from './gh.mjs';
import * as state from './state.mjs';
import { doPlan, doImplement, doMerge, doCancel } from './actions.mjs';

const APPROVE_RE = /(^|[^a-z])(lgtm|continue)([^a-z]|$)/i;
const MERGE_RE = /合并|(^|[^a-z])(merge|ship)([^a-z]|$)/i;
const CANCEL_RE = /(^|[^a-z])(cancel|stop)([^a-z]|$)|停止/i;

export function isMention(text, login) {
  if (!text) return false;
  const escaped = login.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  // GitHub logins may contain '-', so a trailing hyphen is part of a longer
  // login and must not count as a mention of the shorter one.
  return new RegExp(`@${escaped}(?![A-Za-z0-9-])`, 'i').test(String(text));
}

export function parseCommand(text) {
  const body = String(text || '');
  const compact = body.replace(/\s+/g, ' ').trim();
  // Commands are expected in short comments (with or without an @mention),
  // not buried inside long review feedback.
  if (compact.length > 200) return null;
  if (CANCEL_RE.test(compact)) return 'cancel';
  if (MERGE_RE.test(compact)) return 'merge';
  if (APPROVE_RE.test(compact)) return 'approve';
  return null;
}

export function isRelevantIssue(issue, agentLogin) {
  if (!issue || issue.pull_request) return false;
  if ((issue.user?.login || '').toLowerCase() === agentLogin.toLowerCase()) return false;
  const assigned = (issue.assignees || []).some(
    (assignee) => assignee.login.toLowerCase() === agentLogin.toLowerCase()
  );
  const mentioned = isMention(`${issue.title || ''}\n${issue.body || ''}`, agentLogin);
  return assigned || mentioned;
}

export function createRouter({ config, contexts, agentLogin, log }) {
  const queue = [];
  const active = new Set();
  const queuedKeys = new Set();

  function enqueue(key, job) {
    if (active.has(key) || queuedKeys.has(key)) {
      log.debug(`skip duplicate job ${key}`);
      return;
    }
    queuedKeys.add(key);
    queue.push({ key, job });
    pump();
  }

  async function pump() {
    while (queue.length > 0 && active.size < Math.max(1, config.maxParallel)) {
      const { key, job } = queue.shift();
      active.add(key);
      queuedKeys.delete(key);
      job()
        .catch((error) => log.error(`job ${key} failed: ${error.message}`))
        .finally(() => {
          active.delete(key);
          setImmediate(pump);
        });
    }
  }

  async function markBlocked(ctx, number, error) {
    ctx.log.error(`job for ${ctx.repo.fullName}#${number} failed: ${error.message}`);
    state.setIssue(ctx.repo.fullName, number, {
      phase: 'blocked',
      blockedReason: String(error.message).slice(0, 500)
    });
    await gh.setPhaseLabel(ctx.repo.fullName, number, 'agent:blocked');
    await gh.postComment(
      ctx.repo.fullName,
      number,
      `### 🤖 流程出错\n\n\`\`\`\n${String(error.message).slice(0, 800)}\n\`\`\`\n\n回复 **continue** 重试。`
    );
  }

  function getContext(fullName) {
    const ctx = contexts.get(fullName);
    if (!ctx) throw new Error(`no context for repo ${fullName}`);
    return ctx;
  }

  async function handleTrigger(fullName, trigger) {
    const ctx = getContext(fullName);
    const repo = ctx.repo;

    if (trigger.deliveryId) {
      if (state.isDeliveryProcessed(fullName, trigger.deliveryId)) return;
      state.markDeliveryProcessed(fullName, trigger.deliveryId);
    }

    if ((trigger.sender || '').toLowerCase() === agentLogin.toLowerCase()) return;

    if (trigger.type === 'comment') {
      if (state.isCommentProcessed(fullName, trigger.issueNumber, trigger.commentId)) return;
      state.markCommentProcessed(fullName, trigger.issueNumber, trigger.commentId);

      const st = state.getIssue(fullName, trigger.issueNumber);
      const mentioned = isMention(trigger.commentBody, agentLogin);
      const isApprover = repo.approvers.some(
        (login) => login.toLowerCase() === (trigger.sender || '').toLowerCase()
      );
      const command = isApprover ? parseCommand(trigger.commentBody) : null;

      log.info(
        `comment trigger ${fullName}#${trigger.issueNumber} from @${trigger.sender} mentioned=${mentioned} command=${command || '-'}`
      );

      if (command === 'cancel' && st && ['new', 'planning', 'awaiting_approval', 'blocked', 'implementing', 'pr_open'].includes(st.phase)) {
        enqueue(`${fullName}#${trigger.issueNumber}`, async () => {
          const issue = await gh.getIssue(fullName, trigger.issueNumber);
          await doCancel(ctx, trigger, issue, st);
        });
        return;
      }

      if (command === 'merge' && st && (st.phase === 'pr_open' || (st.phase === 'blocked' && st.prNumber))) {
        enqueue(`${fullName}#${trigger.issueNumber}`, async () => {
          const issue = await gh.getIssue(fullName, trigger.issueNumber);
          await doMerge(ctx, trigger, issue, st);
        });
        return;
      }

      if (
        command === 'approve' &&
        st &&
        ['awaiting_approval', 'blocked', 'cancelled'].includes(st.phase)
      ) {
        enqueue(`${fullName}#${trigger.issueNumber}`, async () => {
          const issue = await gh.getIssue(fullName, trigger.issueNumber);
          await doImplement(ctx, trigger, issue, st);
        });
        return;
      }

      if (mentioned && (!st || ['new'].includes(st.phase))) {
        enqueue(`${fullName}#${trigger.issueNumber}`, async () => {
          const issue = await gh.getIssue(fullName, trigger.issueNumber);
          if (isRelevantIssue(issue, agentLogin) || mentioned) {
            try {
              await doPlan(ctx, trigger, issue);
            } catch (error) {
              await markBlocked(ctx, trigger.issueNumber, error);
            }
          }
        });
        return;
      }

      log.info(`comment on ${fullName}#${trigger.issueNumber} ignored (phase=${st?.phase || 'none'})`);
      return;
    }

    if (trigger.type === 'issue') {
      const st = state.getIssue(fullName, trigger.issueNumber);
      if (st && !['new', 'planning'].includes(st.phase)) {
        log.debug(`issue trigger for ${fullName}#${trigger.issueNumber} ignored (phase=${st.phase})`);
        return;
      }
      enqueue(`${fullName}#${trigger.issueNumber}`, async () => {
        const issue = await gh.getIssue(fullName, trigger.issueNumber);
        if (issue.state !== 'open') return;
        if (!isRelevantIssue(issue, agentLogin)) return;
        const comments = await gh.listComments(fullName, trigger.issueNumber);
        state.setIssue(fullName, trigger.issueNumber, {
          phase: 'new',
          processedComments: comments.map((comment) => comment.id),
          lastCommentId: comments.at(-1)?.id || 0
        });
        try {
          await doPlan(ctx, trigger, issue);
        } catch (error) {
          await markBlocked(ctx, trigger.issueNumber, error);
        }
      });
    }
  }

  async function startupScan() {
    for (const [fullName, ctx] of contexts) {
      log.info(`startup scan ${fullName}`);
      const issues = await gh.listOpenIssues(fullName);
      for (const issue of issues) {
        const number = issue.number;
        let st = state.getIssue(fullName, number);

        if ((!st || ['new', 'planning'].includes(st.phase)) && isRelevantIssue(issue, agentLogin)) {
          if (!st) {
            const comments = await gh.listComments(fullName, number);
            state.setIssue(fullName, number, {
              phase: 'new',
              processedComments: comments.map((comment) => comment.id),
              lastCommentId: comments.at(-1)?.id || 0
            });
          }
          log.info(`startup: planning ${fullName}#${number}`);
          await handleTrigger(fullName, {
            type: 'issue',
            issueNumber: number,
            sender: issue.user.login,
            key: `startup-${issue.updated_at}`
          });
          continue;
        }

        if (!st) continue;

        if (st.phase === 'implementing') {
          state.setIssue(fullName, number, { phase: 'blocked', blockedReason: 'interrupted by restart' });
          await gh.setPhaseLabel(fullName, number, 'agent:blocked');
          await gh.postComment(
            fullName,
            number,
            '### 🤖 实现被重启中断\n\n回复 **continue** 从当前 worktree 继续修复。'
          );
        }

        // Replay comments that arrived while the daemon was offline.
        const comments = await gh.listComments(fullName, number);
        const fresh = comments.filter((comment) => !state.isCommentProcessed(fullName, number, comment.id));
        for (const comment of fresh) {
          await handleTrigger(fullName, {
            type: 'comment',
            issueNumber: number,
            commentId: comment.id,
            commentBody: comment.body,
            sender: comment.user.login,
            key: `scan-${comment.id}`
          });
        }
        if (comments.length > 0) {
          state.setIssue(fullName, number, {
            processedComments: comments.map((c) => c.id).filter((id, index, all) => all.indexOf(id) === index),
            lastCommentId: comments.at(-1).id
          });
        }
      }
      state.setRepoMeta(fullName, { lastPollAt: new Date().toISOString() });
    }
  }

  async function pollOnce(fullName) {
    const meta = state.getRepoMeta(fullName);
    let since;
    if (meta.lastPollAt) {
      const safe = new Date(meta.lastPollAt).getTime() - 60_000;
      since = new Date(Math.max(safe, 0)).toISOString();
    }

    const issues = await gh.listOpenIssues(fullName, { since });
    for (const issue of issues) {
      const number = issue.number;
      const st = state.getIssue(fullName, number);
      if (!st && isRelevantIssue(issue, agentLogin)) {
        await handleTrigger(fullName, {
          type: 'issue',
          issueNumber: number,
          sender: issue.user.login,
          key: `poll-${issue.updated_at}`
        });
        continue;
      }
      if (!st) continue;
      const comments = await gh.listComments(fullName, number);
      const fresh = comments.filter((comment) => !state.isCommentProcessed(fullName, number, comment.id));
      for (const comment of fresh) {
        await handleTrigger(fullName, {
          type: 'comment',
          issueNumber: number,
          commentId: comment.id,
          commentBody: comment.body,
          sender: comment.user.login,
          key: `poll-${comment.id}`
        });
      }
      if (comments.length > 0 && fresh.length > 0) {
        const processed = new Set([...(st.processedComments || []), ...comments.map((c) => c.id)]);
        state.setIssue(fullName, number, {
          processedComments: [...processed].slice(-500),
          lastCommentId: comments.at(-1).id
        });
      }
    }
    state.setRepoMeta(fullName, { lastPollAt: new Date().toISOString() });
  }

  return { handleTrigger, startupScan, pollOnce, isRelevantIssue };
}
