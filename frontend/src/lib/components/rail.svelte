<script lang="ts">
	import { t } from '#lib/i18n.svelte.ts';
	import { phaseMeta } from '#lib/phases.ts';
	import type { Task } from '#lib/types.ts';

	let { task }: { task: Task } = $props();

	const labels = ['rail.plan', 'rail.approve', 'rail.implement', 'rail.checks', 'rail.pr', 'rail.merge'];

	let effective = $derived.by(() => {
		const meta = phaseMeta(task.phase);
		if (task.phase === 'blocked') return task.prNumber ? 5 : 3;
		return meta.step;
	});

	function stepClass(step: number): string {
		if (task.phase === 'cancelled') return 'skip';
		if (task.phase === 'done') return 'done';
		if (task.phase === 'blocked' && step === effective) return 'blocked';
		if (step < effective) return 'done';
		if (step === effective) return 'now';
		return 'future';
	}
</script>

<ol class="flex items-center" aria-hidden="true">
	{#each Array(6) as _, index (index)}
		{@const step = index + 1}
		{@const state = stepClass(step)}
		<li
			class="relative flex size-[26px] items-center justify-center first:[&::before]:left-1/2 last:[&::before]:right-1/2 [&::before]:absolute [&::before]:inset-x-0 [&::before]:top-1/2 [&::before]:h-px [&::before]:bg-border [&::before]:content-['']"
			title={t(labels[index])}
		>
			<i
				class="relative z-10 size-2 rounded-full {state === 'done'
					? 'bg-phase-done'
					: state === 'now'
						? 'bg-phase-work ring-2 ring-phase-work/25'
						: state === 'blocked'
							? 'bg-phase-blocked ring-2 ring-phase-blocked/25'
							: 'bg-border'} {state === 'skip' ? 'opacity-30' : ''}"
			></i>
		</li>
	{/each}
</ol>
