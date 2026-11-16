<script lang="ts">
	import { openExternal } from '#lib/api.ts';
	import { dashboard } from '#lib/dashboard.svelte.ts';
	import { applyLanguage, i18n, t } from '#lib/i18n.svelte.ts';
	import { applyTheme, theme, type ThemeMode } from '#lib/theme.svelte.ts';
	import { humanAge } from '#lib/utils.ts';
	import { cn } from '#lib/utils.ts';
	import Button from '#lib/components/ui/button.svelte';

	let { onOpenSettings }: { onOpenSettings: () => void } = $props();

	const themeModes: ThemeMode[] = ['auto', 'light', 'dark'];
	const themeKey: Record<ThemeMode, string> = {
		auto: 'theme.auto',
		light: 'theme.light',
		dark: 'theme.dark'
	};

	function stat(key: string, value: string | number, label: string) {
		return { key, value: String(value), label };
	}

	let stats = $derived.by(() => {
		const data = dashboard.data;
		return [
			stat(
				'daemon',
				data && !dashboard.down
					? 'live'
					: dashboard.down
						? `down · retry ${dashboard.failures}`
						: 'live',
				t('stat.daemon')
			),
			stat('active', data?.active ?? 0, t('stat.active')),
			stat('queued', data?.queued ?? 0, t('stat.queued')),
			stat(
				'waiting',
				data?.waiting ?? (data ? data.tasks.filter((task) => dashboard.needsMe(task)).length : 0),
				t('stat.waiting')
			),
			stat('uptime', data ? humanAge(data.uptimeSec) : '0m', t('stat.uptime'))
		];
	});
</script>

<header class="border-b bg-card/40" data-testid="masthead">
	<div class="flex flex-wrap items-end gap-x-7 gap-y-3 px-4 py-3.5 sm:px-7">
		<div class="flex items-center gap-3">
			<img
				class="size-9 flex-none rounded-md border border-border bg-background"
				src="/logo.svg"
				alt=""
				width="36"
				height="36"
			/>
			<div>
				<h1 class="font-sans text-[34px] leading-none font-bold tracking-tight">
					gh-issue-killer
				</h1>
				<p class="mt-1 max-w-[48ch] font-sans text-[12px] font-medium tracking-[0.04em] text-muted-foreground">
					{t('tagline')}
				</p>
			</div>
		</div>

		<div class="ml-auto flex flex-wrap items-end gap-x-4 gap-y-2" data-testid="status-strip">
			{#each stats as item}
				<div class="flex min-w-[64px] flex-col gap-0.5">
					<b class="flex items-center font-sans text-[19px] leading-none font-semibold tabular-nums">
						{#if item.key === 'daemon'}
							<span
								class={cn(
									'mr-1.5 inline-block size-2 rounded-full',
									dashboard.down ? 'bg-phase-blocked' : 'bg-phase-done'
								)}
								data-testid="daemon-lamp"
							></span>
						{/if}
						{item.value}
					</b>
					<small class="text-[10px] tracking-[0.04em] text-muted-foreground">{item.label}</small>
				</div>
			{/each}
		</div>

		<div class="flex flex-wrap items-start gap-2" data-testid="toolbar">
			<Button
				variant="outline"
				size="sm"
				class="h-6 px-2 text-[10.5px]"
				data-testid="settings-open"
				onclick={onOpenSettings}
			>
				{t('settings.open')}
			</Button>
			<Button
				variant="outline"
				size="sm"
				class="h-6 px-2 text-[10.5px]"
				aria-label={t('github.aria')}
				data-testid="github-link"
				onclick={() => openExternal('https://github.com/uiautodev/gh-issue-killer')}
			>
				<svg viewBox="0 0 16 16" aria-hidden="true" class="size-3.5 fill-current">
					<path
						d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8Z"
					/>
				</svg>
				<span>{t('github.label')}</span>
			</Button>

			<div
				class="flex h-6 items-stretch overflow-hidden rounded-md border border-input"
				role="group"
				aria-label={t('lang.aria')}
				data-testid="lang-toggle"
			>
				{#each [{ id: 'zh', label: '中文' }, { id: 'en', label: 'EN' }] as option}
					<button
						type="button"
						class={cn(
							'border-l border-input px-2 text-[10.5px] text-muted-foreground first:border-l-0 hover:text-foreground',
							i18n.lang === option.id && 'bg-accent text-foreground'
						)}
						aria-pressed={i18n.lang === option.id}
						onclick={() => applyLanguage(option.id as 'zh' | 'en')}
					>
						{option.label}
					</button>
				{/each}
			</div>

			<div
				class="flex h-6 items-stretch overflow-hidden rounded-md border border-input"
				role="group"
				aria-label={t('theme.aria')}
				data-testid="theme-toggle"
			>
				{#each themeModes as mode}
					<button
						type="button"
						class={cn(
							'border-l border-input px-2 text-[10.5px] text-muted-foreground first:border-l-0 hover:text-foreground',
							theme.mode === mode && 'bg-accent text-foreground'
						)}
						aria-pressed={theme.mode === mode}
						data-theme={mode}
						onclick={() => applyTheme(mode)}
					>
						{t(themeKey[mode])}
					</button>
				{/each}
			</div>
		</div>
	</div>

	<div class="flex flex-wrap gap-x-4 gap-y-1 px-4 pb-2 text-[10.5px] text-muted-foreground sm:px-7">
		<span>version <b class="font-medium text-foreground/80">{dashboard.data?.version || '–'}</b></span>
		<span>pid <b class="font-medium text-foreground/80">{dashboard.data?.pid ?? '–'}</b></span>
		<span>identity <b class="font-medium text-foreground/80">{dashboard.data ? '@' + dashboard.data.identity : '–'}</b></span>
		<span>
			transport
			<b class="font-medium text-foreground/80">
				{dashboard.data
					? dashboard.data.webhook
						? t('transport.smee')
						: t('transport.poll')
					: '–'}
			</b>
		</span>
		<span>config <b class="font-medium text-foreground/80">{dashboard.data?.configFile || '–'}</b></span>
	</div>
</header>
