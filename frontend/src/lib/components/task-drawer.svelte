<script lang="ts">
	import X from '@lucide/svelte/icons/x';
	import { fly } from 'svelte/transition';
	import { openExternal } from '#lib/api.ts';
	import { dashboard } from '#lib/dashboard.svelte.ts';
	import { i18n, t } from '#lib/i18n.svelte.ts';
	import { renderMarkdown } from '#lib/markdown.ts';
	import { cn, fmtTime } from '#lib/utils.ts';
	import Button from '#lib/components/ui/button.svelte';

	let task = $derived(dashboard.selectedTask);
	let issue = $derived(dashboard.issue);
	let canApprove = $derived(
		issue?.phase === 'awaiting_approval' || issue?.phase === 'blocked'
	);
	let canCancel = $derived(
		!!issue &&
			['new', 'planning', 'awaiting_approval', 'blocked', 'implementing', 'pr_open'].includes(
				issue.phase
			)
	);
</script>

{#if task}
	<aside
		class="fixed inset-y-0 right-0 z-20 flex w-[min(460px,92vw)] flex-col border-l bg-card shadow-2xl"
		aria-label={t('drawer.aria')}
		data-testid="drawer"
		transition:fly={{ x: 460, duration: 180 }}
	>
		<div class="flex items-start gap-3 border-b px-4.5 py-4">
			<div class="min-w-0">
				<h2 class="font-sans text-[20px] leading-tight font-semibold break-words">
					{task.title || 'issue #' + task.number}
				</h2>
				<div class="mt-1 text-[10.5px] break-words text-muted-foreground">
					{task.repo} #{task.number}
				</div>
			</div>
			<Button
				variant="ghost"
				size="icon"
				class="ml-auto flex-none"
				aria-label={t('drawer.close')}
				data-testid="drawer-close"
				onclick={() => dashboard.closeDrawer()}
			>
				<X class="size-4" />
			</Button>
		</div>

		<div class="flex-1 overflow-auto px-4.5 pt-3.5 pb-7" data-testid="drawer-body">
			{#if dashboard.drawerError}
				<div
					class="my-2 border-l-2 border-phase-blocked bg-phase-blocked/10 px-2.5 py-1.5 text-[11px] whitespace-pre-wrap"
				>
					{t('drawer.failed', { msg: dashboard.drawerError })}
				</div>
			{/if}

			{#if issue}
				<div class="flex flex-wrap gap-3.5 text-[11px]">
					{#if task.url}
						<button
							type="button"
							class="text-primary hover:underline hover:underline-offset-2"
							onclick={() => openExternal(task.url)}
						>
							{t('drawer.openIssue')}
						</button>
					{/if}
					{#if task.prUrl}
						<button
							type="button"
							class="text-primary hover:underline hover:underline-offset-2"
							onclick={() => openExternal(task.prUrl)}
						>
							{t('drawer.openPR', { n: task.prNumber })}
						</button>
					{/if}
				</div>

				{#if issue.blockedReason}
					<div
						class="my-2 border-l-2 border-phase-blocked bg-phase-blocked/10 px-2.5 py-1.5 text-[11px] whitespace-pre-wrap"
						data-testid="blocked-reason"
					>
						{issue.blockedReason}
					</div>
				{/if}

				<h3 class="band-title mt-5 mb-1.5">{t('drawer.status')}</h3>
				<dl class="grid grid-cols-[92px_1fr] gap-x-2.5 gap-y-1 text-[11px]">
					<dt class="text-muted-foreground">{t('drawer.phase')}</dt>
					<dd class="break-words">{dashboard.phaseLabel(issue.phase)}</dd>
					<dt class="text-muted-foreground">{t('drawer.started')}</dt>
					<dd class="break-words">{fmtTime(issue.startedAt, i18n.lang)}</dd>
					<dt class="text-muted-foreground">{t('drawer.updated')}</dt>
					<dd class="break-words">{fmtTime(issue.updatedAt, i18n.lang)}</dd>
					<dt class="text-muted-foreground">{t('drawer.branch')}</dt>
					<dd class="break-words">{issue.branch || '–'}</dd>
					<dt class="text-muted-foreground">{t('drawer.worktree')}</dt>
					<dd class="break-words">{issue.worktree || '–'}</dd>
					<dt class="text-muted-foreground">{t('drawer.session')}</dt>
					<dd class="break-words">{issue.sessionId || '–'}</dd>
				</dl>

				{#if issue.checks && issue.checks.length}
					<h3 class="band-title mt-5 mb-1.5">{t('drawer.checks')}</h3>
					<table class="w-full border-collapse text-[11px]">
						<thead>
							<tr>
								<th class="border-b px-1.5 py-1 text-left font-medium text-muted-foreground">{t('drawer.name')}</th>
								<th class="border-b px-1.5 py-1 text-left font-medium text-muted-foreground">{t('drawer.result')}</th>
								<th class="border-b px-1.5 py-1 text-left font-medium text-muted-foreground">{t('drawer.duration')}</th>
							</tr>
						</thead>
						<tbody>
							{#each issue.checks as check (check.name + check.cmd)}
								<tr>
									<td class="border-b border-border/60 px-1.5 py-1 align-top">
										{#if check.outputTail}
											<details>
												<summary class="cursor-pointer text-muted-foreground">
													{check.name || check.cmd}
												</summary>
												<pre class="mt-1.5 mb-0 text-[10px] whitespace-pre-wrap break-words text-muted-foreground"
													>{check.outputTail}</pre
												>
											</details>
										{:else}
											{check.name || check.cmd}
										{/if}
									</td>
									<td
										class={cn(
											'border-b border-border/60 px-1.5 py-1 align-top',
											check.ok ? 'text-phase-done' : 'text-phase-blocked'
										)}
									>
										{check.ok ? t('drawer.pass') : t('drawer.fail')}
									</td>
									<td class="border-b border-border/60 px-1.5 py-1 align-top">
										{check.durationSec || 0}s
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				{/if}

				{#if issue.screenshots && issue.screenshots.length}
					<h3 class="band-title mt-5 mb-1.5">{t('drawer.screenshots')}</h3>
					<div class="grid gap-2.5">
						{#each issue.screenshots as shot (shot.url)}
							<figure class="m-0">
								<img
									class="block w-full rounded-md border"
									src={shot.url}
									alt={shot.caption || t('drawer.shotAlt')}
									loading="lazy"
								/>
								<figcaption class="mt-1 text-[10px] text-muted-foreground">
									{shot.caption || ''}
								</figcaption>
							</figure>
						{/each}
					</div>
				{/if}

				{#if issue.extraNotes}
					<h3 class="band-title mt-5 mb-1.5">{t('drawer.notes')}</h3>
					<div
						class="my-2 border-l-2 border-phase-work bg-phase-work/10 px-2.5 py-2 text-[11.5px] whitespace-pre-wrap"
					>
						{issue.extraNotes}
					</div>
				{/if}

				{#if issue.plan}
					<h3 class="band-title mt-5 mb-1.5">{t('drawer.plan')}</h3>
					<div
						class="md max-h-[420px] overflow-auto rounded-md border bg-background px-3.5 py-3 text-[11.5px] leading-relaxed"
						data-testid="drawer-plan"
					>
						{@html renderMarkdown(issue.plan)}
					</div>
				{/if}
			{:else if !dashboard.drawerError}
				<p class="py-6 text-[11px] text-muted-foreground">…</p>
			{/if}
		</div>

		{#if issue && (canApprove || canCancel)}
			<div class="flex flex-wrap items-center gap-2.5 border-t px-4.5 py-3" data-testid="drawer-actions">
				{#if canApprove}
					<Button
						variant="outline"
						class="border-phase-work/60 text-phase-work"
						disabled={dashboard.actionBusy}
						onclick={() => dashboard.runAction('approve')}
					>
						{issue.phase === 'blocked' ? t('action.continue') : t('action.approve')}
					</Button>
				{/if}
				{#if canCancel}
					<Button
						variant="destructive"
						disabled={dashboard.actionBusy}
						onclick={() => dashboard.runAction('cancel')}
					>
						{t('action.cancel')}
					</Button>
				{/if}
				<span
					class={cn(
						'ml-auto text-[10.5px] text-muted-foreground',
						dashboard.actionFeedback.kind === 'ok' && 'text-phase-done',
						dashboard.actionFeedback.kind === 'fail' && 'text-phase-blocked'
					)}
					role="status"
				>
					{dashboard.actionFeedback.text}
				</span>
			</div>
		{/if}
	</aside>
{/if}
