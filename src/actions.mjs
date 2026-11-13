import path from 'node:path';
import fs from 'node:fs';
import * as gh from './gh.mjs';
import * as state from './state.mjs';
import { renderPrompt } from './prompts.mjs';
import { runOpencode } from './opencode.mjs';
import {
  fetchBase,
  createWorktree,
  createDetachedWorktree,
  removeWorktree,
  commitAll,
  pushBranch,
  deleteLocalBranch,
  logSinceBase,
  diffStat,
  headSha
} from './git.mjs';
import { loadChecks, runChecks, formatChecksForComment } from './checks.mjs';
import { collectScreenshots, screenshotsMarkdown } from './screenshots.mjs';
import { RUN_DIR, ensureDir, readJsonSync, tail, shortId, nowIso } from './util.mjs';

const SECRET_PATTERNS = [
  /(gho|ghp|ghu|ghs|ghr)_[A-Za-z0-9]{20,}/g,
  /github_pat_[A-Za-z0-9_]{20,}/g
];

function sanitize(text) {
  let out = String(text ?? '');
  for (const pattern of SECRET_PATTERNS) out = out.replace(pattern, '[REDACTED]');
  out = out.replace(/Bearer\s+[A-Za-z0-9._-]{15,}/g, 'Bearer [REDACTED]');
  return out;
}

function stripFences(text) {
  const trimmed = String(text ?? '').trim();
  const match = trimmed.match(/^```(?:markdown|md)?\s*\n([\s\S]*?)\n```$/);
  return match ? match[1].trim() : trimmed;
}

function commentsBlock(comments) {
  if (!comments || comments.length === 0) return '(no comments)';
  return comments
    .map((comment) => `--- @${comment.user.login} at ${comment.created_at}:\n${comment.body || '(empty)'}`)
    .join('\n\n');
}

function runDirFor(ctx, issueNumber) {
  const dir = path.join(RUN_DIR, `${ctx.repo.owner}-${ctx.repo.name}`, `issue-${issueNumber}`);
  ensureDir(dir);
  return dir;
}

async function react(ctx, trigger, content) {
  try {
    if (trigger.commentId) {
      await gh.reactComment(ctx.repo.fullName, trigger.commentId, content);
    } else {
      await gh.reactIssue(ctx.repo.fullName, trigger.issueNumber, content);
    }
  } catch (error) {
    ctx.log.warn(`reaction failed: ${error.message}`);
  }
}

function issueMeta(issue) {
  return {
    number: issue.number,
    title: issue.title,
    body: issue.body || '',
    author: issue.user.login,
    url: issue.html_url,
    labels: (issue.labels || []).map((label) => label.name)
  };
}

export async function doPlan(ctx, trigger, issue) {
  const { repo } = ctx;
  const number = issue.number;
  ctx.log.info(`plan start for ${repo.fullName}#${number}`);
  await react(ctx, trigger, 'eyes');
  state.setIssue(repo.fullName, number, { phase: 'planning', issue: issueMeta(issue) });
  await gh.setPhaseLabel(repo.fullName, number, 'agent:planning');

  const comments = await gh.listComments(repo.fullName, number);
  const prompt = renderPrompt('plan', {
    REPO: repo.fullName,
    NUMBER: String(number),
    TITLE: issue.title,
    BODY: issue.body || '(no description)',
    COMMENTS: commentsBlock(comments)
  });

  await fetchBase(ctx.local, ctx.local.base);
  const planDir = await createDetachedWorktree(ctx.local, `${repo.name}-${number}-${shortId(trigger.key)}`);
  const logFile = path.join(runDirFor(ctx, number), 'plan-opencode.log');
  let plan = '';
  let sessionId = null;

  try {
    const result = await runOpencode({
      cwd: planDir,
      prompt,
      model: repo.model,
      agent: repo.agent,
      env: ctx.childEnv,
      timeoutMs: ctx.config.planTimeoutMin * 60_000,
      logFile,
      log: ctx.log
    });
    plan = stripFences(result.text);
    sessionId = result.sessionId;
  } catch (error) {
    ctx.log.error(`plan failed: ${error.message}`);
    await removeWorktree(ctx.local, planDir, { force: true });
    state.setIssue(repo.fullName, number, { phase: 'blocked', blockedReason: sanitize(error.message) });
    await gh.setPhaseLabel(repo.fullName, number, 'agent:blocked');
    await gh.postComment(
      repo.fullName,
      number,
      `### 🤖 计划生成失败\n\n\`\`\`\n${sanitize(tail(error.message, 20))}\n\`\`\`\n\n回复 **continue** 重试。`
    );
    return;
  }
  await removeWorktree(ctx.local, planDir, { force: true });

  if (!plan || plan.length < 40) {
    state.setIssue(repo.fullName, number, { phase: 'blocked', blockedReason: 'empty plan' });
    await gh.setPhaseLabel(repo.fullName, number, 'agent:blocked');
    await gh.postComment(repo.fullName, number, `### 🤖 计划为空\n\nopencode 没有返回有效计划，回复 **continue** 重试。`);
    return;
  }
  if (plan.length > 55_000) plan = `${plan.slice(0, 55_000)}\n\n…(截断)`;

  const body = [
    '### 🤖 修复计划',
    '',
    plan,
    '',
    '---',
    '确认请回复 **lgtm** 或 **continue**，我会新建 worktree 开始实现；回复 **cancel** 取消。 / Reply **lgtm** / **continue** to proceed.'
  ].join('\n');

  const posted = await gh.postComment(repo.fullName, number, body);
  state.setIssue(repo.fullName, number, {
    phase: 'awaiting_approval',
    plan,
    planCommentId: posted.id,
    sessionId,
    lastCommentId: Math.max(posted.id, comments.at(-1)?.id || 0)
  });
  await gh.setPhaseLabel(repo.fullName, number, 'agent:awaiting-approval');
  ctx.log.info(`plan posted for ${repo.fullName}#${number} (comment ${posted.id})`);
}

export async function doImplement(ctx, trigger, issue, st) {
  const { repo } = ctx;
  const number = issue.number;
  ctx.log.info(`implement start for ${repo.fullName}#${number}`);
  await react(ctx, trigger, 'rocket');
  state.setIssue(repo.fullName, number, { phase: 'implementing', startedAt: nowIso() });
  await gh.setPhaseLabel(repo.fullName, number, 'agent:in-progress');

  await fetchBase(ctx.local, ctx.local.base);
  const key = shortId(st.planCommentId || `${number}-${Date.now()}`);
  const { dir: worktree, branch } = await createWorktree(ctx.local, { issueNumber: number, key });
  const runDir = runDirFor(ctx, number);
  ensureDir(path.join(runDir, 'screenshots'));
  const logFile = path.join(runDir, 'implement-opencode.log');
  state.setIssue(repo.fullName, number, { worktree, branch });

  const comments = await gh.listComments(repo.fullName, number);
  const prompt = renderPrompt('implement', {
    REPO: repo.fullName,
    WORKTREE: worktree,
    BASE: ctx.local.base,
    RUN_DIR: runDir,
    NUMBER: String(number),
    TITLE: issue.title,
    BODY: issue.body || '(no description)',
    COMMENTS: commentsBlock(comments),
    PLAN: st.plan || '(missing plan, implement the issue directly)'
  });

  let sessionId = null;
  try {
    const result = await runOpencode({
      cwd: worktree,
      prompt,
      model: repo.model,
      agent: repo.agent,
      env: ctx.childEnv,
      timeoutMs: ctx.config.implementTimeoutMin * 60_000,
      logFile,
      log: ctx.log
    });
    sessionId = result.sessionId;
  } catch (error) {
    ctx.log.error(`implement failed: ${error.message}`);
    state.setIssue(repo.fullName, number, { phase: 'blocked', blockedReason: sanitize(error.message) });
    await gh.setPhaseLabel(repo.fullName, number, 'agent:blocked');
    await gh.postComment(
      repo.fullName,
      number,
      `### 🤖 实现中断\n\n\`\`\`\n${sanitize(tail(error.message, 20))}\n\`\`\`\n\nworktree 保留在 \`${worktree}\`，回复 **continue** 重试。`
    );
    return;
  }
  state.setIssue(repo.fullName, number, { sessionId });

  let checks = loadChecks({ worktree, runDir, log: ctx.log });
  let outcome = await runChecks({ checks, worktree, runDir, env: ctx.childEnv, log: ctx.log });
  let attempt = 0;

  while (!outcome.ok && attempt < ctx.config.maxFixRounds) {
    attempt += 1;
    ctx.log.warn(`checks failed, fix round ${attempt}/${ctx.config.maxFixRounds}`);
    const failures = outcome.results
      .filter((item) => !item.ok)
      .map((item) => `### ${item.name} (exit ${item.code}${item.timedOut ? ', timeout' : ''})\n\`\`\`\n${tail(item.outputTail, 40)}\n\`\`\``)
      .join('\n\n');
    try {
      await runOpencode({
        cwd: worktree,
        prompt: renderPrompt('fix', {
          RUN_DIR: runDir,
          ATTEMPT: String(attempt),
          MAX_ATTEMPTS: String(ctx.config.maxFixRounds),
          FAILURES: failures,
          WORKTREE: worktree
        }),
        sessionId,
        model: repo.model,
        agent: repo.agent,
        env: ctx.childEnv,
        timeoutMs: ctx.config.implementTimeoutMin * 60_000,
        logFile: path.join(runDir, `fix-${attempt}-opencode.log`),
        log: ctx.log
      });
    } catch (error) {
      ctx.log.error(`fix round ${attempt} failed: ${error.message}`);
    }
    checks = loadChecks({ worktree, runDir, log: ctx.log });
    outcome = await runChecks({ checks, worktree, runDir, env: ctx.childEnv, log: ctx.log });
  }

  if (!outcome.ok) {
    const failures = outcome.results.filter((item) => !item.ok);
    state.setIssue(repo.fullName, number, {
      phase: 'blocked',
      blockedReason: 'checks failed',
      checks: outcome.results
    });
    await gh.setPhaseLabel(repo.fullName, number, 'agent:blocked');
    await gh.postComment(
      repo.fullName,
      number,
      [
        '### 🤖 检查未通过，已停止',
        '',
        formatChecksForComment(outcome.results),
        '',
        ...failures.map((item) => `<details><summary>${item.name}</summary>\n\n\`\`\`\n${sanitize(tail(item.outputTail, 40))}\n\`\`\`\n</details>`),
        '',
        `worktree 保留在 \`${worktree}\`。修复后回复 **continue** 重跑。`
      ].join('\n')
    );
    return;
  }

  await commitAll(worktree, ctx.childEnv, `chore: finalize changes for #${number}`);
  const manifest = readJsonSync(path.join(runDir, 'result.json'), {}) || {};
  const shots = await collectScreenshots({
    runDir,
    repoId: ctx.repoInfo.id,
    token: ctx.token,
    proxy: ctx.proxy,
    log: ctx.log
  });
  const shotsMd = screenshotsMarkdown(shots);

  const commits = await logSinceBase(worktree, ctx.local);
  const stat = await diffStat(worktree, ctx.local);

  await pushBranch(worktree, ctx.local, branch);

  const prTitle = manifest.prTitle || `fix: ${issue.title} (#${number})`;
  const prBody = [
    'This is by an automated agent, not a human.',
    '',
    manifest.summary ? `## 变更\n\n${manifest.summary}` : '',
    manifest.changes?.length ? `### 改动\n${manifest.changes.map((c) => `- ${c}`).join('\n')}` : '',
    '',
    `Closes #${number}`,
    '',
    '## 提交',
    '```',
    commits || '(none)',
    '```',
    '',
    '## 检查',
    formatChecksForComment(outcome.results),
    '',
    manifest.tests?.length ? `## 功能验证\n${manifest.tests.map((t) => `- ${t}`).join('\n')}` : '',
    '',
    shotsMd ? `## 截图\n\n${shotsMd}` : '',
    '',
    '```diff',
    stat || '(no diff)',
    '```'
  ]
    .filter(Boolean)
    .join('\n');

  const pr = await gh.createPr(repo.fullName, {
    title: prTitle,
    head: branch,
    base: ctx.local.base,
    body: prBody
  });

  state.setIssue(repo.fullName, number, {
    phase: 'pr_open',
    prNumber: pr.number,
    prUrl: pr.html_url,
    branch,
    worktree,
    checks: outcome.results,
    screenshots: shots
  });
  await gh.setPhaseLabel(repo.fullName, number, 'agent:pr-open');

  await gh.postComment(
    repo.fullName,
    number,
    [
      `### 🤖 实现完成：${pr.html_url}`,
      '',
      manifest.summary || '',
      '',
      '## 检查',
      formatChecksForComment(outcome.results),
      shotsMd ? `\n## 截图\n\n${shotsMd}` : '',
      '',
      '确认无误回复 **合并**，我会 squash merge 到主干并关闭本 issue。'
    ].join('\n')
  );
  ctx.log.info(`PR opened for ${repo.fullName}#${number}: ${pr.html_url}`);
}

async function waitForChecks(ctx, sha, { timeoutMs = 10 * 60_000, log }) {
  const started = Date.now();
  for (;;) {
    const response = await gh.listCheckRuns(ctx.repo.fullName, sha);
    const runs = Array.isArray(response?.check_runs) ? response.check_runs : null;
    if (!runs) return { ok: true, note: 'no check-runs access, skipping CI gate' };
    if (runs.length === 0) return { ok: true, note: 'no CI configured' };
    const pending = runs.filter((run) => run.status !== 'completed');
    if (pending.length > 0) {
      if (Date.now() - started > timeoutMs) {
        return { ok: false, note: `checks still pending after ${Math.round(timeoutMs / 60000)} min` };
      }
      log.info(`waiting for ${pending.length} checks...`);
      await new Promise((resolve) => setTimeout(resolve, 15_000));
      continue;
    }
    const failed = runs.filter(
      (run) => !['success', 'neutral', 'skipped'].includes(run.conclusion)
    );
    if (failed.length > 0) {
      return { ok: false, note: failed.map((run) => `${run.name}: ${run.conclusion}`).join(', ') };
    }
    return { ok: true, note: `${runs.length} checks passed` };
  }
}

export async function doMerge(ctx, trigger, issue, st) {
  const { repo } = ctx;
  const number = issue.number;
  ctx.log.info(`merge start for ${repo.fullName}#${number} (PR ${st.prNumber})`);
  await react(ctx, trigger, 'hooray');

  const pr = await gh.getPr(repo.fullName, st.prNumber);
  if (pr.state !== 'open') {
    state.setIssue(repo.fullName, number, { phase: 'done' });
    await gh.setPhaseLabel(repo.fullName, number, 'agent:done');
    return;
  }

  const sha = pr.head?.sha;
  if (sha) {
    const checks = await waitForChecks(ctx, sha, { log: ctx.log });
    if (!checks.ok) {
      state.setIssue(repo.fullName, number, { phase: 'blocked', blockedReason: checks.note });
      await gh.setPhaseLabel(repo.fullName, number, 'agent:blocked');
      await gh.postComment(
        repo.fullName,
        number,
        `### 🤖 暂不合并\n\nCI 未通过或仍在等待：${checks.note}\n\n解决后回复 **合并** 重试。`
      );
      return;
    }
    ctx.log.info(`merge gate: ${checks.note}`);
  }

  const merged = await gh.mergePr(repo.fullName, st.prNumber, { method: 'squash' });
  const mergeOk =
    merged && (merged.merged === true || /merged/i.test(JSON.stringify(merged)));
  if (!mergeOk) {
    const detail = merged?.error || JSON.stringify(merged).slice(0, 300);
    state.setIssue(repo.fullName, number, { phase: 'blocked', blockedReason: sanitize(detail) });
    await gh.setPhaseLabel(repo.fullName, number, 'agent:blocked');
    await gh.postComment(
      repo.fullName,
      number,
      `### 🤖 合并失败\n\n\`\`\`\n${sanitize(String(detail))}\n\`\`\`\n\n处理冲突后回复 **合并** 重试。`
    );
    return;
  }

  await gh.deleteBranch(repo.fullName, st.branch);
  const currentIssue = await gh.getIssue(repo.fullName, number);
  if (currentIssue.state === 'open') {
    await gh.updateIssue(repo.fullName, number, { state: 'closed', state_reason: 'completed' });
  }
  if (st.worktree) {
    await removeWorktree(ctx.local, st.worktree, { force: false });
    if (st.branch) await deleteLocalBranch(ctx.local, st.branch);
  }

  state.setIssue(repo.fullName, number, { phase: 'done', mergedAt: nowIso() });
  await gh.setPhaseLabel(repo.fullName, number, 'agent:done');
  await gh.postComment(
    repo.fullName,
    number,
    `### 🤖 已完成\n\n- PR #${st.prNumber} 已 squash merge 到 \`${ctx.local.base}\`\n- 分支 \`${st.branch}\` 已删除，worktree 已清理\n\n本 issue 由自动化流程关闭。`
  );
  ctx.log.info(`merged ${repo.fullName}#${number}`);
}

export async function doCancel(ctx, trigger, issue, st) {
  const { repo } = ctx;
  const number = issue.number;
  ctx.log.info(`cancel for ${repo.fullName}#${number}`);
  await react(ctx, trigger, 'confused');
  if (st?.worktree) {
    await removeWorktree(ctx.local, st.worktree, { force: false });
    if (st.branch) await deleteLocalBranch(ctx.local, st.branch);
  }
  state.setIssue(repo.fullName, number, { phase: 'cancelled', cancelledAt: nowIso() });
  await gh.setPhaseLabel(repo.fullName, number, 'agent:cancelled');
  await gh.postComment(repo.fullName, number, '### 🤖 已取消\n\n回复 **continue** 可以重新开始。');
}
