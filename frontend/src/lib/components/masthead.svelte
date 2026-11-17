<script lang="ts">
	import Settings2 from '@lucide/svelte/icons/settings-2';
	import { openExternal } from '#lib/api.ts';
	import { dashboard } from '#lib/dashboard.svelte.ts';
	import { applyLanguage, i18n, t } from '#lib/i18n.svelte.ts';
	import { applyTheme, theme, type ThemeMode } from '#lib/theme.svelte.ts';
	import { cn, humanAge } from '#lib/utils.ts';
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
				dashboard.down
					? t('stat.down', { n: dashboard.failures })
					: t('stat.live'),
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

<header
	class="sticky top-0 z-30 border-b bg-background/85 backdrop-blur-md"
	data-testid="masthead"
>
	<div
		class="mx-auto flex max-w-[1600px] flex-wrap items-center gap-x-4 gap-y-3 px-4 py-3 sm:px-6"
	>
		<div class="flex min-w-0 items-center gap-3">
			<img
				class="size-10 flex-none rounded-lg border border-border bg-background shadow-xs"
				src="/logo.svg"
				alt=""
				width="40"
				height="40"
			/>
			<div class="min-w-0">
				<h1 class="font-sans text-2xl leading-none font-bold tracking-tight">
					gh-issue-killer
				</h1>
				<p class="mt-1 text-xs font-medium text-muted-foreground">
					{t('tagline')}
				</p>
			</div>
		</div>

		<div class="ml-auto flex flex-wrap items-center gap-2" data-testid="toolbar">
			<Button
				variant="default"
				size="default"
				data-testid="settings-open"
				onclick={onOpenSettings}
			>
				<Settings2 class="size-4" aria-hidden="true" />
				<span class="max-sm:sr-only">{t('settings.open')}</span>
			</Button>
			<a
				href="https://github.com/uiautodev/gh-issue-killer"
				target="_blank"
				rel="noreferrer"
				class="inline-flex h-9 items-center gap-2 rounded-md border border-input bg-background px-3 text-sm font-medium whitespace-nowrap transition-colors hover:bg-accent hover:text-accent-foreground"
				aria-label={t('github.aria')}
				data-testid="github-link"
				onclick={(event) => {
					event.preventDefault();
					openExternal('https://github.com/uiautodev/gh-issue-killer');
				}}
			>
				<svg viewBox="0 0 16 16" aria-hidden="true" class="size-4 fill-current">
					<path
						d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8Z"
					/>
				</svg>
				<span class="max-sm:sr-only">{t('github.label')}</span>
			</a>

			<div
				class="flex h-10 items-stretch overflow-hidden rounded-md border border-input bg-card"
				role="group"
				aria-label={t('lang.aria')}
				data-testid="lang-toggle"
			>
				{#each [{ id: 'zh', label: '中文' }, { id: 'en', label: 'EN' }] as option}
					<button
						type="button"
						class={cn(
							'border-l border-input px-2.5 text-xs text-muted-foreground first:border-l-0 hover:bg-accent hover:text-foreground',
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
				class="flex h-10 items-stretch overflow-hidden rounded-md border border-input bg-card"
				role="group"
				aria-label={t('theme.aria')}
				data-testid="theme-toggle"
			>
				{#each themeModes as mode}
					<button
						type="button"
						class={cn(
							'border-l border-input px-2.5 text-xs text-muted-foreground first:border-l-0 hover:bg-accent hover:text-foreground',
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

	<div class="border-t border-border/60 bg-card/40">
		<dl
			class="mx-auto grid max-w-[1600px] grid-cols-3 gap-x-4 gap-y-2 px-4 py-2 sm:grid-cols-5 sm:px-6"
			data-testid="status-strip"
		>
			{#each stats as item}
				<div class="flex min-w-0 flex-col">
					<dt class="text-2xs tracking-[0.1em] text-muted-foreground uppercase">
						{item.label}
					</dt>
					<dd
						class="flex items-center font-sans text-lg leading-tight font-semibold tabular-nums"
					>
						{#if item.key === 'daemon'}
							<span
								class={cn(
									'mr-1.5 inline-block size-2 rounded-full',
									dashboard.down ? 'bg-phase-blocked' : 'bg-phase-done'
								)}
								data-testid="daemon-lamp"
							></span>
						{/if}
						<span class="truncate text-sm font-medium sm:text-base">{item.value}</span>
					</dd>
				</div>
			{/each}
		</dl>
	</div>
</header>
