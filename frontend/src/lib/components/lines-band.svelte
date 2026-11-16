<script lang="ts">
	import { dashboard } from '#lib/dashboard.svelte.ts';
	import { t } from '#lib/i18n.svelte.ts';
	import { phaseBg } from '#lib/phase-styles.ts';
	import { phaseMeta } from '#lib/phases.ts';
	import type { RepoInfo } from '#lib/types.ts';
	import { humanAge } from '#lib/utils.ts';

	let { repos, planLimit, workLimit }: { repos: RepoInfo[]; planLimit: number; workLimit: number } =
		$props();

	function planActive(repo: RepoInfo): number {
		return (dashboard.data?.tasks || []).filter(
			(task) => task.repo === repo.name && task.active && task.phase === 'planning'
		).length;
	}

	function workActive(repo: RepoInfo): number {
		return (dashboard.data?.tasks || []).filter(
			(task) =>
				task.repo === repo.name &&
				task.active &&
				(task.phase === 'implementing' || task.phase === 'merging')
		).length;
	}

	function slots(limit: number, active: number): number {
		return Math.min(Math.max(limit, active, 1), 12);
	}

	function pollAge(raw: string): string {
		if (!raw) return t('line.pollNever');
		return t('line.poll', {
			age: humanAge(Math.floor((Date.now() - Date.parse(raw)) / 1000))
		});
	}
</script>

<section class="border-b px-4 py-2.5 sm:px-7" data-testid="lines">
	<h2 class="band-title mb-2">{t('band.lines')}</h2>
	<div class="grid gap-px overflow-hidden rounded-md border bg-border sm:grid-cols-2 lg:grid-cols-3">
		{#each repos as repo (repo.name)}
			<div class="flex min-w-0 flex-col gap-1.5 bg-card px-3 py-2" data-testid="repo-line">
				<div class="flex min-w-0 items-baseline gap-2">
					<span class="truncate font-sans text-[16px] font-semibold">{repo.name}</span>
					<span class="ml-auto text-[10px] whitespace-nowrap text-muted-foreground">
						{repo.agent || 'opencode'}
					</span>
				</div>

				<div class="flex flex-wrap gap-x-2.5 gap-y-1 text-[10px] text-muted-foreground">
					{#if Object.keys(repo.counts || {}).length === 0}
						<span>{t('line.idle')}</span>
					{:else}
						{#each Object.entries(repo.counts || {}) as [phase, count] (phase)}
							<span class="inline-flex items-center gap-1">
								<i
									class="inline-block size-1.5 rounded-[1px] {phaseBg[phaseMeta(phase).cls]}"
								></i>
								{dashboard.phaseLabel(phase)}
								<b class="font-medium text-foreground">{count}</b>
							</span>
						{/each}
					{/if}
				</div>

				<div class="flex flex-wrap gap-4" title={t('line.limitHint')}>
					{#each [{ label: t('line.plan'), active: planActive(repo), limit: planLimit }, { label: t('line.work'), active: workActive(repo), limit: workLimit }] as meter}
						<div class="flex flex-col gap-0.5">
							<div class="flex items-baseline gap-1.5">
								<span class="font-sans text-[9px] tracking-[0.1em] text-muted-foreground">{meter.label}</span>
								<span class="text-[9px] text-muted-foreground tabular-nums">{meter.active}/{meter.limit}</span>
							</div>
							<div class="flex gap-0.5">
								{#each Array(slots(meter.limit, meter.active)) as _, index (index)}
									<span class="cell {index < meter.active ? 'on' : ''}"></span>
								{/each}
							</div>
						</div>
					{/each}
				</div>

				<div class="flex flex-wrap gap-x-3 gap-y-0.5 text-[10px] text-muted-foreground">
					<span>queue P{repo.queue.plan} W{repo.queue.work}</span>
					<span>{t('line.issues', { n: repo.issues })}</span>
					<span>{pollAge(repo.lastPoll)}</span>
					{#if repo.base}<span class="truncate">{repo.base}</span>{/if}
				</div>
			</div>
		{/each}
	</div>
</section>
