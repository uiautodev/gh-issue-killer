<script lang="ts">
	import ChevronDown from '@lucide/svelte/icons/chevron-down';
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

<section
	class="mx-auto w-full max-w-[1600px] px-4 pt-4 sm:px-6"
	data-testid="lines"
>
	<details class="card group overflow-hidden" open>
		<summary
			class="flex cursor-pointer list-none flex-wrap items-center gap-x-3 gap-y-1 px-4 py-3 [&::-webkit-details-marker]:hidden"
		>
			<h2 class="band-title">{t('band.lines')}</h2>
			<span class="text-xs text-muted-foreground">{t('band.linesHint')}</span>
			<span class="ml-auto text-xs text-muted-foreground tabular-nums">{repos.length}</span>
			<ChevronDown
				class="size-4 text-muted-foreground transition-transform group-open:rotate-180"
				aria-hidden="true"
			/>
		</summary>

		<div class="grid gap-3 border-t p-3 sm:grid-cols-2 xl:grid-cols-3">
			{#each repos as repo (repo.name)}
				<div
					class="flex min-w-0 flex-col gap-2 rounded-md border bg-background/60 p-3"
					data-testid="repo-line"
				>
					<div class="flex min-w-0 items-baseline gap-2">
						<span class="truncate font-sans text-base font-semibold">{repo.name}</span>
						<span class="ml-auto text-xs whitespace-nowrap text-muted-foreground">
							{repo.agent || 'opencode'}
						</span>
					</div>

					<div class="flex flex-wrap gap-1.5 text-xs text-muted-foreground">
						{#if Object.keys(repo.counts || {}).length === 0}
							<span>{t('line.idle')}</span>
						{:else}
							{#each Object.entries(repo.counts || {}) as [phase, count] (phase)}
								<span
									class="inline-flex items-center gap-1 rounded-full border px-2 py-0.5"
								>
									<i
										class="inline-block size-1.5 rounded-full {phaseBg[phaseMeta(phase).cls]}"
									></i>
									{dashboard.phaseLabel(phase)}
									<b class="font-medium text-foreground tabular-nums">{count}</b>
								</span>
							{/each}
						{/if}
					</div>

					<div class="flex flex-wrap gap-5" title={t('line.limitHint')}>
						{#each [{ label: t('line.plan'), active: planActive(repo), limit: planLimit }, { label: t('line.work'), active: workActive(repo), limit: workLimit }] as meter}
							<div class="flex flex-col gap-1">
								<div class="flex items-baseline gap-1.5">
									<span class="font-sans text-2xs tracking-[0.1em] text-muted-foreground">{meter.label}</span>
									<span class="text-2xs text-muted-foreground tabular-nums">{meter.active}/{meter.limit}</span>
								</div>
								<div class="flex gap-1">
									{#each Array(slots(meter.limit, meter.active)) as _, index (index)}
										<span class="cell {index < meter.active ? 'on' : ''}"></span>
									{/each}
								</div>
							</div>
						{/each}
					</div>

					<div class="flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
						<span>queue P{repo.queue.plan} W{repo.queue.work}</span>
						<span>{t('line.issues', { n: repo.issues })}</span>
						<span>{pollAge(repo.lastPoll)}</span>
						{#if repo.base}<span class="truncate">{repo.base}</span>{/if}
					</div>
				</div>
			{/each}
		</div>
	</details>
</section>
