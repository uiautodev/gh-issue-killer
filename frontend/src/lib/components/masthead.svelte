<script lang="ts">
	import { onMount } from 'svelte';
	import GalleryHorizontal from '@lucide/svelte/icons/gallery-horizontal';
	import Languages from '@lucide/svelte/icons/languages';
	import Monitor from '@lucide/svelte/icons/monitor';
	import Moon from '@lucide/svelte/icons/moon';
	import Settings2 from '@lucide/svelte/icons/settings-2';
	import Sun from '@lucide/svelte/icons/sun';
	import { openExternal } from '#lib/api.ts';
	import { dashboard } from '#lib/dashboard.svelte.ts';
	import { applyLanguage, i18n, t } from '#lib/i18n.svelte.ts';
	import { layout, toggleLayout } from '#lib/layout.svelte.ts';
	import { applyTheme, theme, type ThemeMode } from '#lib/theme.svelte.ts';
	import { cn, formatStars, humanAge, waitingNeedsAttention } from '#lib/utils.ts';
	import Button from '#lib/components/ui/button.svelte';

	let { onOpenSettings }: { onOpenSettings: () => void } = $props();

	const REPO_URL = 'https://github.com/uiautodev/issuekiller';
	// Anonymous api.github.com allows 60 requests per hour and IP, so the count
	// is refreshed once a day and a failed refresh keeps whatever is cached.
	const STARS_KEY = 'issuekiller-stars';
	const STARS_URL = 'https://api.github.com/repos/uiautodev/issuekiller';
	const STARS_TTL_MS = 24 * 60 * 60 * 1000;

	const themeModes: ThemeMode[] = ['auto', 'light', 'dark'];
	const themeKey: Record<ThemeMode, string> = {
		auto: 'theme.auto',
		light: 'theme.light',
		dark: 'theme.dark'
	};
	const themeIcons = { auto: Monitor, light: Sun, dark: Moon };

	let stars = $state<number | null>(null);
	let ThemeIcon = $derived(themeIcons[theme.mode]);
	let themeLabel = $derived(t('theme.toggle', { mode: t(themeKey[theme.mode]) }));
	let layoutLabel = $derived(
		t('layout.toggle', {
			mode: t(layout.width === 'narrow' ? 'layout.narrow' : 'layout.wide')
		})
	);

	type StarCache = { count: number; at: number };

	function readStarCache(): StarCache | null {
		try {
			const raw = localStorage.getItem(STARS_KEY);
			if (!raw) return null;
			const value = JSON.parse(raw) as Partial<StarCache> | null;
			if (typeof value?.count !== 'number' || typeof value.at !== 'number') return null;
			return { count: value.count, at: value.at };
		} catch {
			return null;
		}
	}

	function cycleTheme(): void {
		const next = themeModes[(themeModes.indexOf(theme.mode) + 1) % themeModes.length];
		applyTheme(next);
	}

	function toggleLanguage(): void {
		applyLanguage(i18n.lang === 'zh' ? 'en' : 'zh');
	}

	onMount(() => {
		const cached = readStarCache();
		if (cached) stars = cached.count;
		if (cached && Date.now() - cached.at < STARS_TTL_MS) return;

		const controller = new AbortController();
		fetch(STARS_URL, {
			headers: { Accept: 'application/vnd.github+json' },
			signal: controller.signal
		})
			.then((response) => (response.ok ? response.json() : null))
			.then((data: { stargazers_count?: unknown } | null) => {
				if (typeof data?.stargazers_count !== 'number') return;
				stars = data.stargazers_count;
				try {
					const cache: StarCache = { count: data.stargazers_count, at: Date.now() };
					localStorage.setItem(STARS_KEY, JSON.stringify(cache));
				} catch {
					/* private mode */
				}
			})
			.catch(() => {
				/* offline or rate limited: keep the cached count, or show the icon alone */
			});
		return () => controller.abort();
	});

	function stat(key: string, value: string | number, label: string) {
		return {
			key,
			value: String(value),
			label,
			attention: key === 'waiting' && waitingNeedsAttention(Number(value))
		};
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
		class="page-shell flex flex-wrap items-center gap-x-4 gap-y-3 px-4 py-3 sm:px-6"
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
					issuekiller
				</h1>
				<p class="mt-1 text-xs font-medium text-muted-foreground">
					{t('tagline')}
				</p>
			</div>
		</div>

		<div class="ml-auto flex flex-wrap items-center gap-1" data-testid="toolbar">
			<a
				href={REPO_URL}
				target="_blank"
				rel="noreferrer"
				class="inline-flex h-9 items-center gap-1.5 rounded-md px-2.5 text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
				aria-label={stars === null
					? t('github.aria')
					: t('github.ariaStars', { n: formatStars(stars) })}
				title={t('github.aria')}
				data-testid="github-link"
				onclick={(event) => {
					event.preventDefault();
					openExternal(REPO_URL);
				}}
			>
				<svg viewBox="0 0 16 16" aria-hidden="true" class="size-4 fill-current">
					<path
						d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8Z"
					/>
				</svg>
				{#if stars !== null}
					<span class="text-xs font-semibold tabular-nums" data-testid="github-stars">
						{formatStars(stars)}
					</span>
				{/if}
			</a>

			<span class="mx-0.5 h-5 w-px flex-none bg-border" aria-hidden="true"></span>

			<Button
				variant="ghost"
				size="icon"
				class="size-9 text-muted-foreground hover:text-foreground"
				aria-label={t('settings.aria')}
				title={t('settings.open')}
				data-testid="settings-open"
				onclick={onOpenSettings}
			>
				<Settings2 class="size-4" aria-hidden="true" />
			</Button>

			<span class="mx-0.5 h-5 w-px flex-none bg-border" aria-hidden="true"></span>

			<Button
				variant="ghost"
				size="sm"
				class="h-9 gap-1.5 px-2.5 text-muted-foreground hover:text-foreground"
				aria-label={t('lang.switch')}
				title={t('lang.switch')}
				data-testid="lang-toggle"
				data-lang={i18n.lang}
				onclick={toggleLanguage}
			>
				<Languages class="size-4" aria-hidden="true" />
				<span class="text-xs font-semibold">{i18n.lang === 'zh' ? '中' : 'EN'}</span>
			</Button>

			<span class="mx-0.5 h-5 w-px flex-none bg-border" aria-hidden="true"></span>

			<Button
				variant="ghost"
				size="icon"
				class="size-9 text-muted-foreground hover:text-foreground"
				aria-label={layoutLabel}
				title={layoutLabel}
				data-testid="layout-toggle"
				data-layout={layout.width}
				onclick={toggleLayout}
			>
				<GalleryHorizontal class="size-4" aria-hidden="true" />
			</Button>

			<span class="mx-0.5 h-5 w-px flex-none bg-border" aria-hidden="true"></span>

			<Button
				variant="ghost"
				size="icon"
				class="size-9 text-muted-foreground hover:text-foreground"
				aria-label={themeLabel}
				title={themeLabel}
				data-testid="theme-toggle"
				data-theme={theme.mode}
				onclick={cycleTheme}
			>
				<ThemeIcon class="size-4" aria-hidden="true" />
			</Button>
		</div>
	</div>

	<div class="border-t border-border/60 bg-card/40">
		<dl
			class="page-shell grid grid-cols-3 gap-x-4 gap-y-2 px-4 py-2 sm:grid-cols-5 sm:px-6"
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
						<span
							class={cn(
								'truncate text-sm font-medium sm:text-base',
								item.attention &&
									'rounded-md bg-phase-work/15 px-1.5 text-base font-bold text-phase-work sm:text-lg',
								item.key === 'waiting' && !item.attention && 'font-normal text-muted-foreground/70'
							)}
							data-testid={`stat-${item.key}`}
							data-highlight={item.attention}
						>
							{item.value}
						</span>
					</dd>
				</div>
			{/each}
		</dl>
	</div>
</header>
