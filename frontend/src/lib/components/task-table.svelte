<script lang="ts">
	import Search from '@lucide/svelte/icons/search';
	import { openExternal } from '#lib/api.ts';
	import { dashboard, type Filter } from '#lib/dashboard.svelte.ts';
	import { t } from '#lib/i18n.svelte.ts';
	import { phaseText } from '#lib/phase-styles.ts';
	import { phaseMeta } from '#lib/phases.ts';
	import type { Task } from '#lib/types.ts';
	import { cn, humanAge } from '#lib/utils.ts';
	import Input from '#lib/components/ui/input.svelte';
	import Select from '#lib/components/ui/select.svelte';
	import Rail from './rail.svelte';

	const filters: { id: Filter; label: string }[] = [
		{ id: 'all', label: 'filter.all' },
		{ id: 'active', label: 'filter.active' },
		{ id: 'wait', label: 'filter.wait' },
		{ id: 'blocked', label: 'filter.blocked' }
	];

	let repoFilterOptions = $derived.by(() => [
		{
			value: '',
			label: `${t('filter.allProjects')} (${dashboard.data?.tasks.length ?? 0})`
		},
		...dashboard.repoOptions.map((option) => ({
			value: option.name,
			label: `${option.name} (${option.count})`
		}))
	]);

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

<main
	class="page-shell flex-1 px-4 pt-4 pb-2 sm:px-6"
	data-testid="board"
>
	<div class="mb-3 flex flex-wrap items-center gap-2">
		<div class="flex flex-wrap items-center gap-2" role="group" aria-label={t('filters.aria')} data-testid="filters">
			{#each filters as option (option.id)}
				<button
					type="button"
					class="chip"
					aria-pressed={dashboard.filter === option.id}
					data-filter={option.id}
					onclick={() => (dashboard.filter = option.id)}
				>
					{t(option.label)}
					<span class="text-xs tabular-nums opacity-70">{dashboard.counts[option.id]}</span>
				</button>
			{/each}
			<button
				type="button"
				class={cn('chip', dashboard.hideFinished && 'border-phase-wait/60 text-phase-wait')}
				aria-pressed={dashboard.hideFinished}
				data-testid="hide-finished"
				onclick={() => (dashboard.hideFinished = !dashboard.hideFinished)}
			>
				{t('filter.hideDone')}
			</button>
		</div>

		<div class="ml-auto flex min-w-0 flex-wrap items-center gap-2 max-sm:w-full">
			<Select
				class="min-w-[10rem] max-w-[16rem] flex-1 max-sm:max-w-none"
				options={repoFilterOptions}
				label={t('repo.aria')}
				testid="repo-filter"
				bind:value={dashboard.repo}
			/>

			<div class="relative min-w-[12rem] flex-1 sm:w-[18rem] sm:flex-none">
				<Search
					class="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground"
					aria-hidden="true"
				/>
				<Input
					class="pl-8"
					type="search"
					placeholder={t('search.placeholder')}
					aria-label={t('search.aria')}
					data-testid="search"
					bind:value={dashboard.search}
				/>
			</div>
		</div>
	</div>

	<div class="mb-2 flex flex-wrap items-baseline gap-x-3 gap-y-1">
		<h2 class="band-title">{t('board.aria')}</h2>
		<span class="text-xs text-muted-foreground tabular-nums" data-testid="task-count">
			{t('tasks.count', { n: dashboard.tasks.length })}
		</span>
	</div>

	<!-- svelte-ignore a11y_no_noninteractive_tabindex -- scrollable regions must be keyboard reachable (WCAG 2.1.1) -->
	<div
		class="table-shell card"
		role="region"
		aria-label={t('board.aria')}
		tabindex="0"
		data-testid="table-shell"
	>
		<table
			class="task-table w-full border-collapse"
			data-testid="task-table"
			style="--label-phase: '{t('th.phase')}'; --label-progress: '{t('th.progress')}'; --label-age: '{t('th.age')}'; --label-pr: '{t('th.pr')}'"
		>
			<caption class="sr-only">{t('board.aria')}</caption>
			<thead>
				<tr>
					{#each ['th.issue', 'th.phase', 'th.progress', 'th.age', 'th.pr'] as heading (heading)}
						<th
							scope="col"
							class="border-b px-3 py-2.5 text-left font-sans text-2xs font-semibold tracking-[0.12em] text-muted-foreground uppercase"
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
							'cursor-pointer border-b border-border/60 align-middle transition-colors hover:bg-accent/40',
							isSelected(task) && 'bg-accent/60 shadow-[inset_3px_0_0_var(--phase-work)]'
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
						<th
							scope="row"
							class="max-w-[460px] min-w-[260px] px-3 py-2.5 text-left font-normal"
						>
							<span class="flex items-center gap-1.5 text-xs whitespace-nowrap text-muted-foreground">
								<i
									class={cn(
										'inline-block size-2 flex-none rounded-full',
										task.active
											? 'animate-pulse bg-phase-work'
											: task.queued
												? 'border border-phase-wait'
												: 'bg-border'
									)}
								></i>
								{#if task.url}
									<a
										href={task.url}
										target="_blank"
										rel="noreferrer"
										class="hover:text-foreground hover:underline hover:underline-offset-2"
										onclick={(event) => {
											event.stopPropagation();
											openExternal(task.url);
											event.preventDefault();
										}}
									>
										{task.repo} #{task.number}
									</a>
								{:else}
									{task.repo} #{task.number}
								{/if}
							</span>
							<span
								class="mt-0.5 line-clamp-2 block text-sm leading-snug font-medium text-foreground"
							>
								{task.title || 'issue #' + task.number}
							</span>
						</th>
						<td class="px-3 py-2.5">
							<span
								class={cn(
									'inline-flex items-center rounded-full border border-current px-2.5 py-0.5 text-xs whitespace-nowrap',
									phaseText[phaseMeta(task.phase).cls]
								)}
							>
								{dashboard.phaseLabel(task.phase)}
							</span>
						</td>
						<td class="px-3 py-2.5">
							<Rail {task} />
						</td>
						<td
							class="px-3 py-2.5 text-xs whitespace-nowrap text-muted-foreground tabular-nums"
						>
							{humanAge(task.elapsedSec)}
						</td>
						<td class="px-3 py-2.5 whitespace-nowrap">
							{#if task.prNumber}
								<a
									href={task.prUrl || '#'}
									target="_blank"
									rel="noreferrer"
									class="text-sm text-phase-wait hover:underline hover:underline-offset-2"
									onclick={(event) => openPR(event, task)}
								>
									#{task.prNumber}
								</a>
							{:else}
								<span class="text-sm text-muted-foreground">–</span>
							{/if}
						</td>
					</tr>
				{:else}
					<tr>
						<td
							colspan="5"
							class="empty-cell px-3 py-14 text-center text-muted-foreground"
							data-testid="empty"
						>
							<strong class="mb-1 block font-sans text-base font-medium text-foreground/80">
								{dashboard.anyFiltered ? t('empty.filtered.title') : t('empty.none.title')}
							</strong>
							<span class="text-sm">
								{dashboard.anyFiltered ? t('empty.filtered.hint') : t('empty.none.hint')}
							</span>
						</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
</main>

<style>
	.table-shell {
		max-block-size: min(68dvh, 760px);
		overflow: auto;
		container-type: inline-size;
	}

	.table-shell thead th {
		position: sticky;
		top: 0;
		z-index: 2;
		background: color-mix(in oklch, var(--card) 92%, var(--background));
	}

	/* Narrow boards switch to cards based on the table's own width, not the
	   viewport, so the same component also works in a future sidebar. */
	@container (width < 760px) {
		.task-table,
		.task-table tbody,
		.task-table tr,
		.task-table th,
		.task-table td {
			display: block;
		}

		.task-table thead {
			display: none;
		}

		.task-table tbody {
			display: flex;
			flex-direction: column;
			gap: 0.625rem;
			padding: 0.625rem;
		}

		.task-table tr {
			border: 1px solid var(--border);
			border-radius: var(--radius-md);
			background: var(--card);
			padding: 0.75rem;
		}

		.task-table tr:last-child {
			border-bottom: 1px solid var(--border);
		}

		.task-table th[scope='row'] {
			min-width: 0;
			max-width: none;
			padding: 0 0 0.5rem;
		}

		.task-table td {
			display: flex;
			align-items: center;
			justify-content: space-between;
			gap: 0.75rem;
			padding: 0.2rem 0;
			border: 0;
		}

		.task-table td::before {
			color: var(--muted-foreground);
			font-size: var(--text-xs);
		}

		.task-table td:nth-child(2)::before {
			content: var(--label-phase);
		}

		.task-table td:nth-child(3)::before {
			content: var(--label-progress);
		}

		.task-table td:nth-child(4)::before {
			content: var(--label-age);
		}

		.task-table td:nth-child(5)::before {
			content: var(--label-pr);
		}

		.task-table td.empty-cell {
			display: block;
			padding: 3rem 0.5rem;
			text-align: center;
		}

		.task-table td.empty-cell::before {
			content: none;
		}
	}
</style>
