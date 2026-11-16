<script lang="ts">
	import { openExternal } from '#lib/api.ts';
	import { dashboard, type Filter } from '#lib/dashboard.svelte.ts';
	import { t } from '#lib/i18n.svelte.ts';
	import { phaseText } from '#lib/phase-styles.ts';
	import { phaseMeta } from '#lib/phases.ts';
	import type { Task } from '#lib/types.ts';
	import { cn, humanAge } from '#lib/utils.ts';
	import Input from '#lib/components/ui/input.svelte';
	import Rail from './rail.svelte';

	const filters: { id: Filter; label: string }[] = [
		{ id: 'all', label: 'filter.all' },
		{ id: 'active', label: 'filter.active' },
		{ id: 'wait', label: 'filter.wait' },
		{ id: 'blocked', label: 'filter.blocked' }
	];

	function isSelected(task: Task): boolean {
		return (
			dashboard.selected?.repo === task.repo && dashboard.selected?.number === task.number
		);
	}

	function openPR(event: MouseEvent, task: Task) {
		event.stopPropagation();
		event.preventDefault();
		if (task.prUrl) openExternal(task.prUrl);
	}
</script>

<main class="flex-1 px-4 pt-3 pb-1 sm:px-7" data-testid="board">
	<div class="mb-2 flex flex-wrap items-center gap-2">
		<div class="flex gap-1" role="group" aria-label={t('filters.aria')} data-testid="filters">
			{#each filters as option (option.id)}
				<button
					type="button"
					class={cn(
						'rounded-md border px-2 py-0.5 text-[10.5px] text-muted-foreground transition-colors hover:text-foreground',
						dashboard.filter === option.id && 'border-primary/50 text-foreground'
					)}
					aria-pressed={dashboard.filter === option.id}
					data-filter={option.id}
					onclick={() => (dashboard.filter = option.id)}
				>
					{t(option.label)} {dashboard.counts[option.id]}
				</button>
			{/each}
		</div>

		<button
			type="button"
			class={cn(
				'rounded-md border px-2 py-0.5 text-[10.5px] text-muted-foreground transition-colors hover:text-foreground',
				dashboard.hideFinished && 'border-phase-wait/60 text-phase-wait'
			)}
			aria-pressed={dashboard.hideFinished}
			data-testid="hide-finished"
			onclick={() => (dashboard.hideFinished = !dashboard.hideFinished)}
		>
			{t('filter.hideDone')}
		</button>

		<select
			class="h-7 max-w-[240px] rounded-md border border-input bg-transparent px-1.5 text-[11px]"
			aria-label={t('repo.aria')}
			data-testid="repo-filter"
			bind:value={dashboard.repo}
		>
			<option value="">{t('filter.allProjects')} ({dashboard.data?.tasks.length ?? 0})</option>
			{#each dashboard.repoOptions as option (option.name)}
				<option value={option.name}>{option.name} ({option.count})</option>
			{/each}
		</select>

		<Input
			class="ml-auto w-[210px] max-sm:ml-0 max-sm:w-full"
			type="search"
			placeholder={t('search.placeholder')}
			aria-label={t('search.aria')}
			data-testid="search"
			bind:value={dashboard.search}
		/>
	</div>

	<table class="w-full border-collapse" data-testid="task-table">
		<thead class="max-sm:hidden">
			<tr>
				{#each ['th.issue', 'th.phase', 'th.progress', 'th.age', 'th.pr'] as heading (heading)}
					<th
						scope="col"
						class="border-b px-2.5 py-1.5 text-left font-sans text-[10.5px] font-semibold tracking-[0.12em] text-muted-foreground"
					>
						{t(heading)}
					</th>
				{/each}
			</tr>
		</thead>
		<tbody>
			{#each dashboard.tasks as task (task.repo + '#' + task.number)}
				<tr
					class={cn(
						'cursor-pointer border-b border-border/60 align-middle transition-colors hover:bg-accent/40 max-sm:grid max-sm:grid-cols-[1fr_auto] max-sm:gap-x-2.5 max-sm:px-1 max-sm:py-1.5',
						isSelected(task) && 'bg-phase-work/10 shadow-[inset_2px_0_0_var(--phase-work)]'
					)}
					tabindex="0"
					data-testid="task-row"
					data-repo={task.repo}
					data-number={task.number}
					onclick={() => dashboard.selectTask(task)}
					onkeydown={(event) => {
						if (event.key === 'Enter' || event.key === ' ') {
							event.preventDefault();
							dashboard.selectTask(task);
						}
					}}
				>
					<td class="max-w-[420px] min-w-[240px] px-2.5 py-2 max-sm:max-w-none max-sm:min-w-0 max-sm:p-0.5">
						<span class="flex items-center gap-1.5 text-[10px] whitespace-nowrap text-muted-foreground">
							<i
								class={cn(
									'inline-block size-[7px] flex-none rounded-full',
									task.active
										? 'animate-pulse bg-phase-work'
										: task.queued
											? 'border border-phase-wait'
											: ''
								)}
							></i>
							{task.repo} #{task.number}
						</span>
						<span class="block truncate text-[12.5px] text-foreground">
							{task.title || 'issue #' + task.number}
						</span>
					</td>
					<td class="px-2.5 py-2 max-sm:p-0.5">
						<span
							class={cn(
								'inline-block rounded-sm border border-current px-1.5 py-0.5 text-[10px] whitespace-nowrap',
								phaseText[phaseMeta(task.phase).cls]
							)}
						>
							{dashboard.phaseLabel(task.phase)}
						</span>
					</td>
					<td class="px-2.5 py-2 max-sm:col-span-2 max-sm:p-0.5">
						<Rail {task} />
					</td>
					<td class="px-2.5 py-2 text-[11px] whitespace-nowrap text-muted-foreground tabular-nums max-sm:inline-block max-sm:p-0.5">
						{humanAge(task.elapsedSec)}
					</td>
					<td class="px-2.5 py-2 whitespace-nowrap max-sm:inline-block max-sm:p-0.5">
						{#if task.prNumber}
							<a
								href={task.prUrl || '#'}
								target="_blank"
								rel="noreferrer"
								class="text-[11px] text-phase-wait hover:underline hover:underline-offset-2"
								onclick={(event) => openPR(event, task)}
							>
								#{task.prNumber}
							</a>
						{:else}
							<span class="text-[11px] text-muted-foreground">–</span>
						{/if}
					</td>
				</tr>
			{:else}
				<tr>
					<td colspan="5" class="px-2.5 py-12 text-center text-muted-foreground" data-testid="empty">
						<strong class="mb-1 block font-sans text-[13px] font-medium text-foreground/80">
							{dashboard.anyFiltered ? t('empty.filtered.title') : t('empty.none.title')}
						</strong>
						<span class="text-[11px]">
							{dashboard.anyFiltered ? t('empty.filtered.hint') : t('empty.none.hint')}
						</span>
					</td>
				</tr>
			{/each}
		</tbody>
	</table>
</main>
