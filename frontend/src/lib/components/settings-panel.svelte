<script lang="ts">
	import X from '@lucide/svelte/icons/x';
	import Plus from '@lucide/svelte/icons/plus';
	import { onDestroy, onMount } from 'svelte';
	import {
		getAuth,
		getConfig,
		getDetect,
		getUpdate,
		openExternal,
		pollDevice,
		postRestart,
		postToken,
		postUpdate,
		putConfig,
		startDevice
	} from '#lib/api.ts';
	import { dashboard } from '#lib/dashboard.svelte.ts';
	import { showModalDialog } from '#lib/dialog.ts';
	import { t } from '#lib/i18n.svelte.ts';
	import { addRepoNames, parseRepoNames, removeRepoName } from '#lib/repos.ts';
	import type {
		AuthPayload,
		ConfigPayload,
		DetectPayload,
		DevicePayload,
		UpdatePayload
	} from '#lib/types.ts';
	import { cn } from '#lib/utils.ts';
	import Button from '#lib/components/ui/button.svelte';
	import Input from '#lib/components/ui/input.svelte';
	import Select from '#lib/components/ui/select.svelte';

	interface Feedback {
		text: string;
		kind: 'idle' | 'ok' | 'fail';
	}

	interface ReleaseRow {
		name: string;
		enabled: boolean;
		interval: string;
		time: string;
	}

	interface ReleaseValue {
		enabled: boolean;
		intervalDays: number;
		time: string;
	}

	// HH:MM in 24-hour form; the browser's time input only emits this, but a
	// config edited by hand may not.
	const RELEASE_TIME = /^([01]\d|2[0-3]):[0-5]\d$/;
	const RELEASE_DEFAULT_TIME = '09:00';

	const AGENT_FALLBACK = [
		{ id: 'opencode', name: 'opencode' },
		{ id: 'claude', name: 'Claude Code' },
		{ id: 'codex', name: 'Codex CLI' }
	];

	let configDoc = $state<Record<string, unknown>>({});
	let configDefaults = $state<Record<string, unknown>>({});
	let configPath = $state('–');
	let configVersion = $state('–');
	let configProblem = $state('');

	let repoText = $state('');
	let repoDraft = $state('');
	let rows = $state<ReleaseRow[]>([]);
	let form = $state({ agent: 'opencode', model: '', proxy: '', webPort: '0', updateProxy: '', selfUpdate: false, followMaster: false });

	let agents = $state<{ id: string; name?: string; version?: string }[]>([]);
	let models = $state<string[]>([]);
	let detected = $state<DetectPayload | null>(null);

	let feedback = $state<Feedback>({ text: '', kind: 'idle' });
	let busy = $state('');

	let auth = $state<AuthPayload | null>(null);
	let authFeedback = $state<Feedback>({ text: '', kind: 'idle' });
	let authBusy = $state(false);
	let authDevice = $state<DevicePayload | null>(null);
	let authToken = $state('');
	let authTimer: ReturnType<typeof setTimeout> | null = null;

	let update = $state<UpdatePayload | null>(null);
	let updateStatus = $state('');

	let dialog = $state<HTMLDialogElement | null>(null);

	$effect(() => {
		if (!dialog) return;
		return showModalDialog(dialog);
	});

	function close(): void {
		dialog?.close();
	}

	let repoNames = $derived(parseRepoNames(repoText));
	let agentOptions = $derived.by<{ id: string; version?: string }[]>(() =>
		agents.length ? agents : AGENT_FALLBACK
	);
	let agentSelectOptions = $derived.by(() => {
		const options = agentOptions.map((agent) => ({
			value: agent.id,
			label: agent.version ? agent.id + ' · ' + agent.version : agent.id
		}));
		// A config may name an agent that is not installed here; keep it
		// selectable instead of silently switching the saved value.
		if (form.agent && !agentOptions.some((agent) => agent.id === form.agent)) {
			options.push({ value: form.agent, label: form.agent });
		}
		return options;
	});

	function parseRepos(text: string): string[] {
		const names = parseRepoNames(text);
		if (!names.length) throw new Error(t('settings.repoRequired'));
		return names;
	}

	function pick(doc: Record<string, unknown>, key: string, fallback: unknown): unknown {
		if (doc[key] !== undefined) return doc[key];
		if (configDefaults[key] !== undefined) return configDefaults[key];
		return fallback;
	}

	function repoList(doc: Record<string, unknown>): string[] {
		const value = doc.repos;
		if (Array.isArray(value)) {
			return value
				.map((item) =>
					typeof item === 'string' ? item : item && typeof item === 'object' ? (item as { repo?: string }).repo : ''
				)
				.filter((name): name is string => Boolean(name));
		}
		if (value && typeof value === 'object') return Object.keys(value).sort();
		return [];
	}

	// Repos may be a plain array, an array of objects with overrides, or an
	// object keyed by name. Keep the overrides of repos that stay, and only
	// add/remove names.
	function setRepoList(doc: Record<string, unknown>, names: string[]): void {
		const original = doc.repos;
		if (Array.isArray(original) && original.some((item) => item && typeof item === 'object')) {
			const byName = new Map<string, Record<string, unknown>>();
			for (const item of original) {
				if (item && typeof item === 'object' && typeof (item as { repo?: string }).repo === 'string') {
					byName.set((item as { repo: string }).repo, { ...(item as Record<string, unknown>) });
				}
			}
			doc.repos = names.map((name) => {
				const existing = byName.get(name);
				return existing ? { ...existing, repo: name } : { repo: name };
			});
			return;
		}
		if (!original || Array.isArray(original)) {
			doc.repos = names;
			return;
		}
		const next: Record<string, unknown> = {};
		for (const name of names) {
			const existing = (original as Record<string, unknown>)[name];
			next[name] = existing && typeof existing === 'object' ? existing : {};
		}
		doc.repos = next;
	}

	function releaseDefaults(): ReleaseValue {
		const defaults =
			(configDefaults.release as
				| { enabled?: boolean; intervalDays?: number; time?: string }
				| undefined) || {};
		const interval = Number(defaults.intervalDays);
		return {
			enabled: defaults.enabled === true,
			intervalDays: Number.isInteger(interval) && interval >= 1 ? interval : 1,
			time:
				typeof defaults.time === 'string' && RELEASE_TIME.test(defaults.time)
					? defaults.time
					: RELEASE_DEFAULT_TIME
		};
	}

	function repoEntry(doc: Record<string, unknown>, name: string): Record<string, unknown> | null {
		const repos = doc.repos;
		if (Array.isArray(repos)) {
			const found = repos.find(
				(item) => item && typeof item === 'object' && (item as { repo?: string }).repo === name
			);
			return found ? (found as Record<string, unknown>) : null;
		}
		if (repos && typeof repos === 'object') {
			const entry = (repos as Record<string, unknown>)[name];
			return entry && typeof entry === 'object' ? (entry as Record<string, unknown>) : null;
		}
		return null;
	}

	function releaseValue(doc: Record<string, unknown>, name: string): ReleaseValue {
		const base = releaseDefaults();
		const entry = repoEntry(doc, name);
		const release =
			entry && entry.release && typeof entry.release === 'object'
				? (entry.release as Record<string, unknown>)
				: {};
		const interval = Number(release.intervalDays);
		return {
			enabled: release.enabled === true,
			intervalDays: Number.isInteger(interval) && interval >= 1 ? interval : base.intervalDays,
			time:
				typeof release.time === 'string' && RELEASE_TIME.test(release.time)
					? release.time
					: base.time
		};
	}

	function releaseIsSet(value: ReleaseValue, defaults: ReleaseValue): boolean {
		return (
			value.enabled ||
			Number(value.intervalDays) !== defaults.intervalDays ||
			value.time !== defaults.time
		);
	}

	// repoObjects turns the array-of-names form into objects so a release block
	// can sit next to codingAgent/model/language. The keyed object form and the
	// array-of-objects form are already per-repo.
	function repoObjects(doc: Record<string, unknown>, names: string[]): void {
		const repos = doc.repos;
		if (repos && !Array.isArray(repos) && typeof repos === 'object') return;
		const existing = new Map<string, Record<string, unknown>>();
		if (Array.isArray(repos)) {
			for (const item of repos) {
				if (item && typeof item === 'object' && typeof (item as { repo?: string }).repo === 'string') {
					existing.set((item as { repo: string }).repo, { ...(item as Record<string, unknown>) });
				} else if (typeof item === 'string') {
					existing.set(item, { repo: item });
				}
			}
		}
		doc.repos = names.map((name) => existing.get(name) || { repo: name });
	}

	function mutableRepoEntry(doc: Record<string, unknown>, name: string): Record<string, unknown> | null {
		const repos = doc.repos;
		if (Array.isArray(repos)) {
			return (
				(repos.find(
					(item) => item && typeof item === 'object' && (item as { repo?: string }).repo === name
				) as Record<string, unknown> | undefined) || null
			);
		}
		if (repos && typeof repos === 'object') {
			const table = repos as Record<string, unknown>;
			if (!table[name] || typeof table[name] !== 'object') table[name] = {};
			return table[name] as Record<string, unknown>;
		}
		return null;
	}

	function applyReleases(doc: Record<string, unknown>, names: string[]): void {
		const defaults = releaseDefaults();
		const values = names.map((name) => {
			const row = rows.find((candidate) => candidate.name === name);
			const value: ReleaseValue = row
				? { enabled: row.enabled, intervalDays: Number(row.interval), time: row.time || defaults.time }
				: releaseValue(doc, name);
			return [name, value] as const;
		});
		if (values.some(([, value]) => releaseIsSet(value, defaults))) repoObjects(doc, names);
		for (const [name, value] of values) {
			const entry = mutableRepoEntry(doc, name);
			if (!entry) continue;
			if (!releaseIsSet(value, defaults)) {
				delete entry.release;
				continue;
			}
			const interval = Number(value.intervalDays);
			const release: Record<string, unknown> = {
				enabled: Boolean(value.enabled),
				intervalDays: Number.isInteger(interval) && interval >= 1 ? interval : defaults.intervalDays
			};
			// The default time stays implicit; only a deliberate schedule is
			// written, so later default changes still reach these repos.
			if (value.time && value.time !== defaults.time) release.time = value.time;
			entry.release = release;
		}
	}

	// releaseRowsProblem returns the i18n key of the first invalid field, or an
	// empty string. The time input cannot produce a bad value, but a pasted or
	// restored draft can, and the PUT would reject it anyway.
	function releaseRowsProblem(names: string[]): string {
		for (const name of names) {
			const row = rows.find((candidate) => candidate.name === name);
			if (!row) continue;
			const interval = Number(row.interval);
			if (!Number.isInteger(interval) || interval < 1) return 'settings.releaseIntervalInvalid';
			if (row.time && !RELEASE_TIME.test(row.time)) return 'settings.releaseTimeInvalid';
		}
		return '';
	}

	// syncRows renders a row per repo while typing and keeps values already
	// entered for repos that stay in the list.
	function syncRows(): void {
		const names = parseRepoNames(repoText);
		rows = names.map((name) => {
			const existing = rows.find((row) => row.name === name);
			if (existing) return existing;
			const value = releaseValue(configDoc, name);
			return { name, enabled: value.enabled, interval: String(value.intervalDays), time: value.time };
		});
	}

	// The list and the text field share repoText; adding or removing a name
	// rebuilds it so saving keeps using the same parser and order.
	function addRepo(): void {
		const next = addRepoNames(repoText, repoDraft);
		repoDraft = '';
		if (next === repoNames.join('\n')) return;
		repoText = next;
		syncRows();
	}

	function removeRepo(name: string): void {
		repoText = removeRepoName(repoText, name);
		syncRows();
	}

	function setFeedback(message: string, kind: Feedback['kind'] = 'idle'): void {
		feedback = { text: message, kind };
	}

	function formError(message: string): void {
		setFeedback(t('settings.failed', { msg: message }), 'fail');
	}

	function applyConfigPayload(data: ConfigPayload): void {
		configDoc = data.config || {};
		configDefaults = data.defaults || {};
		configPath = data.path + (data.exists ? '' : ' · ' + t('settings.newFile'));
		configVersion = data.version || '–';
		configProblem = data.configError || data.error || '';
		repoText = repoList(configDoc).join('\n');
		rows = repoList(configDoc).map((name) => {
			const value = releaseValue(configDoc, name);
			return { name, enabled: value.enabled, interval: String(value.intervalDays), time: value.time };
		});
		const agent = String(pick(configDoc, 'codingAgent', 'opencode'));
		form.agent = agent;
		form.model = String(pick(configDoc, 'model', ''));
		form.proxy = String(pick(configDoc, 'proxy', ''));
		const port = configDoc.webPort !== undefined ? configDoc.webPort : 0;
		form.webPort = String(port);
		form.updateProxy = String(pick(configDoc, 'updateProxy', ''));
		form.selfUpdate = Boolean(pick(configDoc, 'selfUpdate', false));
		form.followMaster = Boolean(pick(configDoc, 'followMaster', false));
		models = [];
		detected = null;
		update = null;
		updateStatus = '';
	}

	async function loadConfig(): Promise<void> {
		try {
			applyConfigPayload(await getConfig());
		} catch (err) {
			formError(err instanceof Error ? err.message : String(err));
		}
	}

	async function saveSettings(event: SubmitEvent): Promise<void> {
		event.preventDefault();
		const doc = JSON.parse(JSON.stringify(configDoc || {})) as Record<string, unknown>;
		let repos: string[];
		try {
			repos = parseRepos(repoText);
		} catch (err) {
			formError(err instanceof Error ? err.message : String(err));
			return;
		}
		const problem = releaseRowsProblem(repos);
		if (problem) {
			formError(t(problem));
			return;
		}
		setRepoList(doc, repos);
		applyReleases(doc, repos);
		const assign = (key: string, value: unknown) => {
			if (value === '' || value === null || value === undefined) delete doc[key];
			else doc[key] = value;
		};
		assign('codingAgent', form.agent);
		assign('model', form.model.trim());
		assign('proxy', form.proxy.trim());
		assign('updateProxy', form.updateProxy.trim());
		const port = Number(form.webPort);
		doc.webPort = Number.isFinite(port) && port >= 0 && port <= 65535 ? Math.trunc(port) : 0;
		doc.selfUpdate = form.selfUpdate;
		doc.followMaster = form.followMaster;

		busy = 'save';
		setFeedback(t('settings.saving'));
		try {
			await putConfig(doc);
			configDoc = doc;
			setFeedback(t('settings.saved'), 'ok');
			if (dashboard.data?.configError) setTimeout(() => dashboard.closeSettings(), 1200);
			setTimeout(() => dashboard.refreshState(), 1500);
		} catch (err) {
			formError(err instanceof Error ? err.message : String(err));
		} finally {
			busy = '';
		}
	}

	async function runDetect(): Promise<void> {
		busy = 'detect';
		setFeedback(t('settings.detecting'));
		try {
			const params = new URLSearchParams({ agent: form.agent || '', proxy: '1' });
			try {
				const repos = parseRepos(repoText);
				if (repos.length) params.set('repos', repos.join(','));
			} catch {
				/* no repos yet: agents/models still work */
			}
			const data = (await getDetect(params)) as DetectPayload;
			agents = data.agents || [];
			models = data.models || [];
			detected = data;
			setFeedback(t('settings.detected'), 'ok');
		} catch (err) {
			formError(err instanceof Error ? err.message : String(err));
		} finally {
			busy = '';
		}
	}

	async function restartEngine(): Promise<void> {
		busy = 'restart';
		setFeedback(t('settings.restarting'));
		try {
			await postRestart();
			setFeedback(t('settings.restarting'), 'ok');
		} catch (err) {
			formError(err instanceof Error ? err.message : String(err));
		} finally {
			busy = '';
		}
	}

	async function checkUpdate(): Promise<void> {
		busy = 'check-update';
		updateStatus = t('settings.updateChecking');
		try {
			update = (await getUpdate()) as UpdatePayload;
			if (update.error) {
				updateStatus = t('settings.updateFailed', { msg: update.error });
			} else {
				updateStatus = update.available
					? t('settings.updateAvailable', { latest: update.latest })
					: t('settings.updateCurrent', { current: update.current });
			}
		} catch (err) {
			updateStatus = t('settings.updateFailed', {
				msg: err instanceof Error ? err.message : String(err)
			});
		} finally {
			busy = '';
		}
	}

	async function installUpdate(): Promise<void> {
		busy = 'install-update';
		updateStatus = t('settings.updateInstalling');
		try {
			const data = (await postUpdate()) as UpdatePayload;
			updateStatus = t('settings.updateInstalled', { latest: data.latest });
		} catch (err) {
			updateStatus = t('settings.updateFailed', {
				msg: err instanceof Error ? err.message : String(err)
			});
		} finally {
			busy = '';
		}
	}

	function authHint(data: AuthPayload | null): string {
		if (!data?.authenticated) return t('auth.hint');
		return t(data.refreshable ? 'auth.hintRefresh' : 'auth.hintBot');
	}

	function applyAuth(data: AuthPayload): void {
		auth = data;
		if (data.pending && data.device) openAuthDevice(data.device);
	}

	function openAuthDevice(device: DevicePayload | null): void {
		authDevice = device;
		if (authTimer) {
			clearTimeout(authTimer);
			authTimer = null;
		}
		if (!device) return;
		authFeedback = { text: t('auth.waiting'), kind: 'idle' };
		const delay = Math.max(2, Math.min(10, device.interval || 5)) * 1000;
		authTimer = setTimeout(pollAuth, delay);
	}

	async function pollAuth(): Promise<void> {
		authTimer = null;
		try {
			const data = (await pollDevice()) as AuthPayload;
			if (data.pending) {
				openAuthDevice(data.device || null);
				return;
			}
			authDevice = null;
			authFeedback = {
				text: t('auth.success', { login: data.login ? '@' + data.login : '' }),
				kind: 'ok'
			};
			auth = data;
		} catch (err) {
			authDevice = null;
			authFeedback = {
				text: t('auth.failed', { msg: err instanceof Error ? err.message : String(err) }),
				kind: 'fail'
			};
		}
	}

	async function login(): Promise<void> {
		authBusy = true;
		authFeedback = { text: t('auth.starting'), kind: 'idle' };
		try {
			applyAuth((await startDevice()) as AuthPayload);
		} catch (err) {
			authFeedback = {
				text: t('auth.failed', { msg: err instanceof Error ? err.message : String(err) }),
				kind: 'fail'
			};
		} finally {
			authBusy = false;
		}
	}

	async function pasteToken(): Promise<void> {
		const token = authToken.trim();
		if (!token) {
			authFeedback = { text: t('auth.failed', { msg: 'token is required' }), kind: 'fail' };
			return;
		}
		authBusy = true;
		authFeedback = { text: t('auth.pasting'), kind: 'idle' };
		try {
			const data = await postToken(token);
			authToken = '';
			authFeedback = {
				text: t('auth.pasted', { login: data.login ? '@' + data.login : '' }),
				kind: 'ok'
			};
			auth = { authenticated: true, login: data.login, source: 'token', refreshable: false };
		} catch (err) {
			authFeedback = {
				text: t('auth.pasteFailed', { msg: err instanceof Error ? err.message : String(err) }),
				kind: 'fail'
			};
		} finally {
			authBusy = false;
		}
	}

	async function loadAuth(): Promise<void> {
		try {
			applyAuth((await getAuth()) as AuthPayload);
		} catch (err) {
			authFeedback = {
				text: t('auth.failed', { msg: err instanceof Error ? err.message : String(err) }),
				kind: 'fail'
			};
		}
	}

	onMount(() => {
		void loadConfig();
		void loadAuth();
	});

	onDestroy(() => {
		if (authTimer) clearTimeout(authTimer);
	});
</script>

<dialog
	bind:this={dialog}
	class="settings-dialog"
	closedby="any"
	aria-labelledby="settings-title"
	data-testid="settings"
	onclose={() => dashboard.closeSettings()}
>
	<div class="flex h-full min-h-0 flex-col">
		<header class="flex items-start gap-3 border-b px-5 py-4">
			<div class="min-w-0">
				<h2 id="settings-title" class="font-sans text-xl font-semibold">{t('settings.title')}</h2>
				<div class="mt-1 text-xs break-words text-muted-foreground" data-testid="settings-path">
					{configPath}
				</div>
			</div>
			<Button
				variant="ghost"
				size="icon"
				class="ml-auto flex-none"
				aria-label={t('settings.close')}
				autofocus
				onclick={close}
			>
				<X class="size-4" />
			</Button>
		</header>

		<div class="flex min-h-0 flex-1">
			<nav
				class="hidden w-40 flex-none flex-col gap-1 border-r p-3 md:flex"
				aria-label={t('settings.aria')}
			>
				{#each [{ id: 'settings-auth', label: 'settings.navAuth' }, { id: 'settings-config', label: 'settings.navConfig' }, { id: 'settings-update', label: 'settings.navUpdate' }] as section}
					<a
						href={'#' + section.id}
						class="rounded-md px-3 py-2 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-foreground"
					>
						{t(section.label)}
					</a>
				{/each}
			</nav>

			<div class="min-h-0 flex-1 overflow-auto px-5 py-4">
				<nav class="mb-4 flex flex-wrap gap-2 md:hidden" aria-label={t('settings.aria')}>
					{#each [{ id: 'settings-auth', label: 'settings.navAuth' }, { id: 'settings-config', label: 'settings.navConfig' }, { id: 'settings-update', label: 'settings.navUpdate' }] as section}
						<a class="chip" href={'#' + section.id}>{t(section.label)}</a>
					{/each}
				</nav>

				<!-- login -->
				<section id="settings-auth" class="flex scroll-mt-4 flex-col gap-2.5 rounded-lg border border-phase-work/30 bg-phase-work/5 p-4" data-testid="auth-panel">
					<div class="flex flex-wrap items-baseline gap-2.5">
						<span class="text-base tracking-[0.04em]">{t('auth.title')}</span>
						<span
							class={cn(
								'text-sm',
								auth?.authenticated ? 'text-phase-done' : auth ? 'text-phase-blocked' : 'text-muted-foreground'
							)}
							data-testid="auth-state"
						>
							{auth
								? auth.authenticated
									? auth.login
										? t('auth.loggedInAs', { login: '@' + auth.login })
										: t('auth.loggedIn')
									: t('auth.loggedOut')
								: '–'}
						</span>
					</div>
					<p class="m-0 text-sm leading-normal text-muted-foreground">{authHint(auth)}</p>
					{#if authDevice}
						<div class="flex flex-wrap items-center gap-3.5 rounded-md border bg-background px-2.5 py-2" data-testid="auth-device">
							<span class="text-2xs tracking-[0.06em] text-muted-foreground">{t('auth.code')}</span>
							<code class="text-lg font-semibold tracking-[0.16em] text-phase-wait">{authDevice.userCode || '----'}</code>
							<Button
								variant="outline"
								onclick={() => openExternal(authDevice?.verificationUri || '')}
							>
								{t('auth.open')}
							</Button>
						</div>
					{/if}
					<div class="flex flex-wrap items-center gap-2">
						<Button variant="outline" disabled={authBusy} onclick={login}>
							{auth?.authenticated ? t('auth.relogin') : t('auth.login')}
						</Button>
						<span
							class={cn(
								'text-xs text-muted-foreground',
								authFeedback.kind === 'ok' && 'text-phase-done',
								authFeedback.kind === 'fail' && 'text-phase-blocked'
							)}
							role="status"
						>
							{authFeedback.text}
						</span>
					</div>
					<details>
						<summary class="cursor-pointer text-sm text-muted-foreground">{t('auth.pasteToggle')}</summary>
						<div class="mt-2 flex flex-wrap items-end gap-2">
							<label class="flex min-w-[150px] flex-1 flex-col gap-1">
								<span class="text-xs tracking-[0.04em] text-muted-foreground">{t('auth.pasteLabel')}</span>
								<Input
									type="password"
									spellcheck="false"
									autocomplete="off"
									placeholder={t('auth.pastePlaceholder')}
									bind:value={authToken}
								/>
							</label>
							<Button variant="outline" disabled={authBusy} onclick={pasteToken}>
								{t('auth.pasteSave')}
							</Button>
						</div>
					</details>
				</section>

				{#if configProblem}
					<div
						class="border-l-2 border-phase-work bg-phase-work/10 px-2.5 py-2 text-sm whitespace-pre-wrap"
						data-testid="settings-note"
					>
						{t('settings.setupHint')}
						{configProblem}
					</div>
				{/if}

				<section id="settings-config" class="mt-6 scroll-mt-4">
					<h3 class="band-title mb-2">{t('settings.navConfig')}</h3>
					<form id="settings-form" class="flex flex-col gap-4" autocomplete="off" onsubmit={saveSettings}>
					<div class="flex flex-col gap-1" data-testid="repo-settings">
						<span class="text-xs tracking-[0.04em] text-muted-foreground">{t('settings.repos')}</span>
						<ul
							class="flex flex-col divide-y divide-border/60 overflow-hidden rounded-md border"
							data-testid="repo-list"
						>
							{#each repoNames as name (name)}
								<li class="flex min-w-0 items-center gap-2 px-2.5 py-1" data-repo={name}>
									<span class="min-w-0 flex-1 truncate text-sm">{name}</span>
									<Button
										type="button"
										variant="ghost"
										size="icon"
										class="size-7 flex-none text-muted-foreground hover:text-destructive"
										aria-label={t('settings.repoRemove', { repo: name })}
										data-testid="repo-remove"
										onclick={() => removeRepo(name)}
									>
										<X class="size-3.5" aria-hidden="true" />
									</Button>
								</li>
							{:else}
								<li class="px-2.5 py-2 text-sm text-muted-foreground">
									{t('settings.reposEmpty')}
								</li>
							{/each}
						</ul>
						<div class="mt-1 flex items-center gap-2">
							<Input
								spellcheck="false"
								placeholder={t('settings.repoAddPlaceholder')}
								aria-label={t('settings.repoAdd')}
								data-testid="repo-add-input"
								bind:value={repoDraft}
								onkeydown={(event) => {
									if (event.key !== 'Enter') return;
									event.preventDefault();
									addRepo();
								}}
							/>
							<Button type="button" variant="outline" onclick={addRepo} data-testid="repo-add">
								<Plus class="size-4" aria-hidden="true" />
								{t('settings.repoAdd')}
							</Button>
						</div>
					</div>

					{#if repoNames.length}
						<div class="flex flex-col gap-2 border-t pt-3" data-testid="release-rows">
							<div class="flex flex-wrap items-baseline gap-2.5">
								<span class="text-xs tracking-[0.04em] text-muted-foreground">{t('settings.release')}</span>
								<small class="text-xs text-muted-foreground">{t('settings.releaseHint')}</small>
							</div>
							{#each rows as row (row.name)}
								<div class="flex flex-wrap items-end gap-2.5 border-b border-border/60 py-1.5 last:border-b-0 last:pb-0" data-repo={row.name}>
									<span class="min-w-[110px] flex-[1_1_150px] break-words pb-1 text-sm">{row.name}</span>
									<label class="flex items-center gap-1.5 pb-1 text-2xs text-muted-foreground">
										<input type="checkbox" class="accent-phase-work" bind:checked={row.enabled} />
										{t('settings.releaseEnabled')}
									</label>
									<label class="flex flex-col gap-1 text-2xs tracking-[0.03em] text-muted-foreground">
										{t('settings.releaseInterval')}
										<input
											type="number"
											min="1"
											step="1"
											class="h-9 w-20 rounded-md border border-input bg-transparent px-2 text-sm"
											bind:value={row.interval}
										/>
									</label>
									<label class="flex flex-col gap-1 text-2xs tracking-[0.03em] text-muted-foreground">
										{t('settings.releaseTime')}
										<input
											type="time"
											class="h-9 w-[7.5rem] rounded-md border border-input bg-transparent px-2 text-sm"
											bind:value={row.time}
										/>
									</label>
								</div>
							{/each}
						</div>
					{/if}

					<div class="flex flex-wrap gap-3">
						<div class="flex min-w-[150px] flex-1 flex-col gap-1">
							<span class="text-xs tracking-[0.04em] text-muted-foreground">{t('settings.agent')}</span>
							<Select
								options={agentSelectOptions}
								label={t('settings.agent')}
								testid="agent-select"
								bind:value={form.agent}
							/>
						</div>
						<label class="flex min-w-[150px] flex-1 flex-col gap-1">
							<span class="text-xs tracking-[0.04em] text-muted-foreground">{t('settings.model')}</span>
							<Input list="cfg-models" placeholder={t('settings.modelHint')} bind:value={form.model} />
							<datalist id="cfg-models">
								{#each models as model (model)}
									<option value={model}></option>
								{/each}
							</datalist>
						</label>
					</div>

					<div class="flex flex-wrap gap-3">
						<label class="flex min-w-[150px] flex-1 flex-col gap-1">
							<span class="text-xs tracking-[0.04em] text-muted-foreground">{t('settings.proxy')}</span>
							<Input spellcheck="false" placeholder="socks5h://127.0.0.1:10808" bind:value={form.proxy} />
						</label>
						<label class="flex min-w-[150px] flex-1 flex-col gap-1">
							<span class="text-xs tracking-[0.04em] text-muted-foreground">{t('settings.webPort')}</span>
							<Input type="number" min="0" max="65535" step="1" placeholder="8787" bind:value={form.webPort} />
						</label>
					</div>

					<div class="flex flex-wrap items-end gap-3">
						<label class="flex min-w-[150px] flex-1 flex-col gap-1">
							<span class="text-xs tracking-[0.04em] text-muted-foreground">{t('settings.updateProxy')}</span>
							<Input spellcheck="false" placeholder="gh-proxy.com" bind:value={form.updateProxy} />
						</label>
						<label class="flex items-center gap-1.5 pb-1 text-sm text-muted-foreground">
							<input type="checkbox" class="accent-phase-work" bind:checked={form.selfUpdate} />
							{t('settings.selfUpdate')}
						</label>
						<label class="flex items-center gap-1.5 pb-1 text-sm text-muted-foreground">
							<input type="checkbox" class="accent-phase-work" bind:checked={form.followMaster} />
							{t('settings.followMaster')}
						</label>
					</div>

					</form>
				</section>

				{#if detected}
					<div class="flex flex-col gap-1 text-xs text-muted-foreground" data-testid="settings-detected">
						<div class="flex flex-wrap gap-3">
							<span>{t('settings.detectedAgents')}:</span>
							{#if detected.agents.length}
								{#each detected.agents as agent (agent.id)}
									<span class="text-phase-done">{agent.version ? agent.id + ' · ' + agent.version : agent.id}</span>
								{/each}
							{:else}
								<span>{t('settings.noAgents')}</span>
							{/if}
						</div>
						{#if detected.proxy}
							<div class="flex flex-wrap gap-3">
								<span>{t('settings.detectedProxy')}:</span>
								<span class="text-phase-done">{detected.proxy}</span>
							</div>
						{/if}
						{#if detected.repos.length}
							<div class="flex flex-wrap gap-3">
								<span>{t('settings.detectedRepos')}:</span>
								{#each detected.repos as repo (repo.repo)}
									<span class={repo.permission === 'admin' || repo.permission === 'write' ? 'text-phase-done' : 'text-phase-blocked'}>
										{repo.repo} · {repo.permission}{repo.error ? ' (' + repo.error + ')' : ''}
									</span>
								{/each}
							</div>
						{/if}
						{#if detected.error}
							<div class="flex flex-wrap gap-3"><span>!</span><span class="text-phase-blocked">{detected.error}</span></div>
						{/if}
					</div>
				{/if}

				<section id="settings-update" class="mt-6 scroll-mt-4">
					<h3 class="band-title mb-2">{t('settings.navUpdate')}</h3>
					<div class="flex flex-col gap-3 rounded-lg border p-4" data-testid="update-card">
						<div class="flex flex-wrap gap-4 text-sm text-muted-foreground">
							<span>version <b class="font-medium text-foreground" data-testid="settings-version">{configVersion}</b></span>
							<span>{updateStatus}</span>
						</div>
						{#if form.followMaster}
							<p class="m-0 text-sm text-muted-foreground" data-testid="update-follow-master">
								{t('settings.updateFollowMaster')}
							</p>
						{:else}
							<div class="flex flex-wrap items-center gap-2">
								<Button variant="outline" disabled={busy === 'check-update'} onclick={checkUpdate}>
									{t('settings.checkUpdate')}
								</Button>
								{#if update?.available}
									<Button variant="default" disabled={busy === 'install-update'} onclick={installUpdate}>
										{t('settings.installUpdate')}
									</Button>
								{/if}
							</div>
						{/if}
					</div>

					<div class="mt-3 rounded-lg border p-4" data-testid="settings-system">
						<h3 class="band-title mb-2">{t('settings.system')}</h3>
						<dl class="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
							<dt class="text-muted-foreground">version</dt>
							<dd class="tabular-nums">{dashboard.data?.version || configVersion}</dd>
							<dt class="text-muted-foreground">{t('settings.pid')}</dt>
							<dd class="tabular-nums">{dashboard.data?.pid ?? '–'}</dd>
							<dt class="text-muted-foreground">{t('settings.identity')}</dt>
							<dd class="break-all">{dashboard.data?.identity ? '@' + dashboard.data.identity : '–'}</dd>
							<dt class="text-muted-foreground">{t('settings.transport')}</dt>
							<dd>
								{dashboard.data
									? dashboard.data.webhook
										? t('transport.smee')
										: t('transport.poll')
									: '–'}
							</dd>
							<dt class="text-muted-foreground">{t('settings.configFile')}</dt>
							<dd class="break-all">{dashboard.data?.configFile || configPath}</dd>
						</dl>
					</div>
				</section>
			</div>
		</div>

		<footer class="flex flex-wrap items-center gap-2 border-t bg-card px-5 py-3">
			<Button type="submit" form="settings-form" variant="default" disabled={busy === 'save'}>
				{t('settings.save')}
			</Button>
			<Button variant="outline" disabled={busy === 'detect'} onclick={runDetect}>{t('settings.detect')}</Button>
			<Button variant="outline" disabled={busy === 'restart'} onclick={restartEngine}>{t('settings.restart')}</Button>
			<Button variant="outline" onclick={loadConfig}>{t('settings.reload')}</Button>
			<span
				class={cn(
					'ml-auto text-xs text-muted-foreground',
					feedback.kind === 'ok' && 'text-phase-done',
					feedback.kind === 'fail' && 'text-phase-blocked'
				)}
				role="status"
				data-testid="settings-result"
			>
				{feedback.text}
			</span>
		</footer>
	</div>
</dialog>

<style>
	.settings-dialog {
		inline-size: min(760px, 94vw);
		max-inline-size: none;
		block-size: min(88dvh, 880px);
		max-block-size: none;
		margin: auto;
		padding: 0;
		border: 1px solid var(--border);
		border-radius: var(--radius-lg);
		background: var(--card);
		color: var(--card-foreground);
		box-shadow: var(--shadow-float);
	}

	.settings-dialog::backdrop {
		background: oklch(0 0 0 / 0.5);
		backdrop-filter: blur(2px);
	}
</style>
