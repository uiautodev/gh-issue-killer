<script lang="ts">
	import { dashboard } from '#lib/dashboard.svelte.ts';
	import { t } from '#lib/i18n.svelte.ts';
	import { cn } from '#lib/utils.ts';
	import Button from '#lib/components/ui/button.svelte';

	let pre = $state<HTMLPreElement | null>(null);
	let stick = true;

	const levelColor: Record<string, string> = {
		INFO: 'text-phase-wait',
		WARN: 'text-phase-work',
		ERROR: 'text-phase-blocked',
		DEBUG: 'text-muted-foreground'
	};

	type LogLine =
		| { kind: 'log'; time: string; level: string; message: string }
		| { kind: 'raw'; text: string };

	let lines = $derived.by<LogLine[]>(() => {
		return dashboard.logText.split('\n').map((line): LogLine => {
			// daemon.log format: 2026-01-02T03:04:05Z INFO  message. The API stays
			// plain text; splitting happens here only for colouring.
			const match = line.match(/^(\S+)\s+(INFO|WARN|ERROR|DEBUG)\s+(.*)$/);
			if (!match) return { kind: 'raw', text: line };
			return { kind: 'log', time: match[1], level: match[2], message: match[3] };
		});
	});

	$effect(() => {
		void dashboard.logText;
		if (pre && stick) pre.scrollTop = pre.scrollHeight;
	});

	function toggleOpen() {
		dashboard.logOpen = !dashboard.logOpen;
		if (dashboard.logOpen) dashboard.refreshLogs();
	}

	function togglePause() {
		dashboard.logPaused = !dashboard.logPaused;
		if (!dashboard.logPaused) dashboard.refreshLogs();
	}
</script>

<section class="border-t bg-card/30 px-4 py-2.5 sm:px-7" data-testid="console">
	<div class="mb-1.5 flex items-center gap-3">
		<h2 class="band-title">{t('band.console')}</h2>
		<span class="flex-1"></span>
		<Button
			variant="outline"
			class="h-6 px-2 text-[10.5px]"
			aria-expanded={dashboard.logOpen}
			data-testid="log-toggle"
			onclick={toggleOpen}
		>
			{dashboard.logOpen ? t('log.collapse') : t('log.expand')}
		</Button>
		<Button
			variant="outline"
			class="h-6 px-2 text-[10.5px]"
			aria-pressed={dashboard.logPaused}
			data-testid="log-pause"
			onclick={togglePause}
		>
			{dashboard.logPaused ? t('log.resume') : t('log.pause')}
		</Button>
	</div>
	<pre
		bind:this={pre}
		class={cn(
			'm-0 max-h-[210px] overflow-auto text-[10.5px] leading-relaxed whitespace-pre-wrap break-words text-muted-foreground',
			!dashboard.logOpen && 'hidden'
		)}
		aria-live="off"
		data-testid="log"
		onscroll={() => {
			if (pre) stick = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 24;
		}}
	>
		{#each lines as line, index (index)}
			{#if line.kind === 'log'}
				<span class="block {levelColor[line.level] || ''}">
					<span class="text-muted-foreground/70">{line.time} </span><span class="font-semibold">{line.level}</span> {line.message}
				</span>
			{:else}
				<span class="block">{line.text + '\n'}</span>
			{/if}
		{/each}
	</pre>
</section>
