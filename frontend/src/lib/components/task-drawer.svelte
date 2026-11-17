<script lang="ts">
	import X from '@lucide/svelte/icons/x';
	import { openExternal } from '#lib/api.ts';
	import { dashboard } from '#lib/dashboard.svelte.ts';
	import { showModalDialog } from '#lib/dialog.ts';
	import { i18n, t } from '#lib/i18n.svelte.ts';
	import { renderMarkdown } from '#lib/markdown.ts';
	import { cn, fmtTime } from '#lib/utils.ts';
	import Button from '#lib/components/ui/button.svelte';

	let dialog = $state<HTMLDialogElement | null>(null);
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

	$effect(() => {
		if (!task || !dialog) return;
		return showModalDialog(dialog);
	});

	function close(): void {
		dialog?.close();
	}
</script>

{#if task}
	<dialog
		bind:this={dialog}
		class="drawer"
		closedby="any"
		aria-labelledby="drawer-title"
		data-testid="drawer"
		onclose={() => dashboard.closeDrawer()}
	>
		<div class="flex h-full min-h-0 flex-col">
			<header class="flex items-start gap-3 border-b px-5 py-4">
				<div class="min-w-0">
					<h2
						id="drawer-title"
						class="font-sans text-xl leading-tight font-semibold break-words"
					>
						{task.title || 'issue #' + task.number}
					</h2>
					<div class="mt-1 text-xs break-words text-muted-foreground">
						{task.repo} #{task.number}
					</div>
				</div>
				<Button
					variant="ghost"
					size="icon"
					class="ml-auto flex-none"
					aria-label={t('drawer.close')}
					data-testid="drawer-close"
					autofocus
					onclick={close}
				>
					<X class="size-4" />
				</Button>
			</header>

			<div class="min-h-0 flex-1 overflow-auto px-5 py-4" data-testid="drawer-body">
				{#if dashboard.drawerError}
					<div
						class="my-2 rounded-r-md border-l-2 border-phase-blocked bg-phase-blocked/10 px-3 py-2 text-sm whitespace-pre-wrap"
					>
						{t('drawer.failed', { msg: dashboard.drawerError })}
					</div>
				{/if}

				{#if issue}
					<div class="flex flex-wrap gap-4 text-sm">
						{#if task.url}
							<a
								href={task.url}
								target="_blank"
								rel="noreferrer"
								class="text-primary hover:underline hover:underline-offset-2"
								onclick={(event) => {
									event.preventDefault();
									openExternal(task.url);
								}}
							>
								{t('drawer.openIssue')}
							</a>
						{/if}
						{#if task.prUrl}
							<a
								href={task.prUrl}
								target="_blank"
								rel="noreferrer"
								class="text-primary hover:underline hover:underline-offset-2"
								onclick={(event) => {
									event.preventDefault();
									openExternal(task.prUrl);
								}}
							>
								{t('drawer.openPR', { n: task.prNumber })}
							</a>
						{/if}
					</div>

					{#if issue.blockedReason}
						<div
							class="my-2 rounded-r-md border-l-2 border-phase-blocked bg-phase-blocked/10 px-3 py-2 text-sm whitespace-pre-wrap"
							data-testid="blocked-reason"
						>
							{issue.blockedReason}
						</div>
					{/if}

					<h3 class="band-title mt-6 mb-2">{t('drawer.status')}</h3>
					<dl class="grid grid-cols-[7rem_1fr] gap-x-3 gap-y-1.5 text-sm">
						<dt class="text-muted-foreground">{t('drawer.phase')}</dt>
						<dd class="break-words">{dashboard.phaseLabel(issue.phase)}</dd>
						<dt class="text-muted-foreground">{t('drawer.started')}</dt>
						<dd class="break-words">{fmtTime(issue.startedAt, i18n.lang)}</dd>
						<dt class="text-muted-foreground">{t('drawer.updated')}</dt>
						<dd class="break-words">{fmtTime(issue.updatedAt, i18n.lang)}</dd>
						<dt class="text-muted-foreground">{t('drawer.branch')}</dt>
						<dd class="break-words">{issue.branch || '–'}</dd>
						<dt class="text-muted-foreground">{t('drawer.worktree')}</dt>
						<dd class="break-all">{issue.worktree || '–'}</dd>
						<dt class="text-muted-foreground">{t('drawer.session')}</dt>
						<dd class="break-all">{issue.sessionId || '–'}</dd>
					</dl>

					{#if issue.checks && issue.checks.length}
						<h3 class="band-title mt-6 mb-2">{t('drawer.checks')}</h3>
						<table class="w-full border-collapse text-sm">
							<thead>
								<tr>
									<th class="border-b px-2 py-1.5 text-left font-medium text-muted-foreground">{t('drawer.name')}</th>
									<th class="border-b px-2 py-1.5 text-left font-medium text-muted-foreground">{t('drawer.result')}</th>
									<th class="border-b px-2 py-1.5 text-left font-medium text-muted-foreground">{t('drawer.duration')}</th>
								</tr>
							</thead>
							<tbody>
								{#each issue.checks as check (check.name + check.cmd)}
									<tr>
										<td class="border-b border-border/60 px-2 py-1.5 align-top">
											{#if check.outputTail}
												<details>
													<summary class="cursor-pointer text-muted-foreground">
														{check.name || check.cmd}
													</summary>
													<pre
														class="mt-2 mb-0 text-xs whitespace-pre-wrap break-words text-muted-foreground"
														>{check.outputTail}</pre
													>
												</details>
											{:else}
												{check.name || check.cmd}
											{/if}
										</td>
										<td
											class={cn(
												'border-b border-border/60 px-2 py-1.5 align-top',
												check.ok ? 'text-phase-done' : 'text-phase-blocked'
											)}
										>
											{check.ok ? t('drawer.pass') : t('drawer.fail')}
										</td>
										<td class="border-b border-border/60 px-2 py-1.5 align-top tabular-nums">
											{check.durationSec || 0}s
										</td>
									</tr>
								{/each}
							</tbody>
						</table>
					{/if}

					{#if issue.screenshots && issue.screenshots.length}
						<h3 class="band-title mt-6 mb-2">{t('drawer.screenshots')}</h3>
						<div class="grid gap-3">
							{#each issue.screenshots as shot (shot.url)}
								<figure class="m-0">
									<img
										class="block w-full rounded-md border"
										src={shot.url}
										alt={shot.caption || t('drawer.shotAlt')}
										loading="lazy"
									/>
									<figcaption class="mt-1 text-xs text-muted-foreground">
										{shot.caption || ''}
									</figcaption>
								</figure>
							{/each}
						</div>
					{/if}

					{#if issue.extraNotes}
						<h3 class="band-title mt-6 mb-2">{t('drawer.notes')}</h3>
						<div
							class="my-2 rounded-r-md border-l-2 border-phase-work bg-phase-work/10 px-3 py-2 text-sm whitespace-pre-wrap"
						>
							{issue.extraNotes}
						</div>
					{/if}

					{#if issue.plan}
						<h3 class="band-title mt-6 mb-2">{t('drawer.plan')}</h3>
						<div
							class="md max-h-[46vh] overflow-auto rounded-md border bg-background px-4 py-3 text-sm leading-relaxed"
							data-testid="drawer-plan"
						>
							{@html renderMarkdown(issue.plan)}
						</div>
					{/if}
				{:else if !dashboard.drawerError}
					<p class="py-8 text-sm text-muted-foreground">…</p>
				{/if}
			</div>

			{#if issue && (canApprove || canCancel)}
				<footer
					class="flex flex-wrap items-center gap-2.5 border-t bg-card px-5 py-3"
					data-testid="drawer-actions"
				>
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
							'ml-auto text-xs text-muted-foreground',
							dashboard.actionFeedback.kind === 'ok' && 'text-phase-done',
							dashboard.actionFeedback.kind === 'fail' && 'text-phase-blocked'
						)}
						role="status"
					>
						{dashboard.actionFeedback.text}
					</span>
				</footer>
			{/if}
		</div>
	</dialog>
{/if}

<style>
	.drawer {
		margin: 0 0 0 auto;
		padding: 0;
		inline-size: min(480px, 94vw);
		max-inline-size: none;
		block-size: 100dvh;
		max-block-size: 100dvh;
		border: 0;
		border-inline-start: 1px solid var(--border);
		background: var(--card);
		color: var(--card-foreground);
		box-shadow: var(--shadow-float);
		opacity: 1;
		transform: translateX(0);
		transition:
			opacity 0.18s ease,
			transform 0.18s ease,
			overlay 0.18s allow-discrete,
			display 0.18s allow-discrete;
	}

	.drawer:not([open]) {
		opacity: 0;
		transform: translateX(1.5rem);
	}

	@starting-style {
		.drawer[open] {
			opacity: 0;
			transform: translateX(1.5rem);
		}
	}

	.drawer::backdrop {
		background: oklch(0 0 0 / 0.45);
		backdrop-filter: blur(2px);
	}
</style>
