<script lang="ts">
	import X from '@lucide/svelte/icons/x';
	import Plus from '@lucide/svelte/icons/plus';
	import Trash2 from '@lucide/svelte/icons/trash-2';
	import { onDestroy, onMount, tick } from 'svelte';
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
	import {
		addRepoNames,
		mutableRepoEntry,
		parseRepoNames,
		repoCodingAgent,
		repoEntry,
		repoObjects,
		repoString,
		removeRepoName,
		setRepoStrings
	} from '#lib/repos.ts';
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

	interface RepoDraft {
		agent: string;
		model: string;
		language: string;
		releaseEnabled: boolean;
		releaseInterval: string;
		releaseTime: string;
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

	// The dialog is a tab interface, not one scrolling page: only the selected
	// section is visible, and the vertical nav plus the horizontal row on narrow
	// screens are one tablist driving the same `tab` value. Selection follows
	// focus, as for native tabs.
	const TABS = [
		{ id: 'auth', label: 'settings.navAuth' },
		{ id: 'repos', label: 'settings.navRepos' },
		{ id: 'config', label: 'settings.navConfig' },
		{ id: 'update', label: 'settings.navUpdate' }
	] as const;

	type TabID = (typeof TABS)[number]['id'];

	let tab = $state<TabID>('config');
	let tabInitialised = $state(false);

	let configDoc = $state<Record<string, unknown>>({});
	let configDefaults = $state<Record<string, unknown>>({});
	let configPath = $state('–');
	let configVersion = $state('–');
	let configProblem = $state('');

	let repoText = $state('');
	let repoDraft = $state('');
	// One draft per watched repo, keyed by name. The repo tab shows the
	// selected one; the others keep their edits while switching, and saving
	// writes all of them so removing a repo never drops another repo's rows.
	// An empty agent/model/language means "follow the global value".
	let drafts = $state<Record<string, RepoDraft>>({});
	let selectedRepo = $state('');
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

	function selectTab(id: TabID): void {
		tab = id;
		// The roving tabindex moves with the selection, so focus the new tab
		// after the DOM update; keyboard and pointer both end up consistent.
		void tick().then(() => document.getElementById('settings-tab-' + id)?.focus());
	}

	function onTabKeydown(event: KeyboardEvent): void {
		const index = TABS.findIndex((section) => section.id === tab);
		let next = index;
		if (event.key === 'ArrowRight' || event.key === 'ArrowDown') next = (index + 1) % TABS.length;
		else if (event.key === 'ArrowLeft' || event.key === 'ArrowUp')
			next = (index - 1 + TABS.length) % TABS.length;
		else if (event.key === 'Home') next = 0;
		else if (event.key === 'End') next = TABS.length - 1;
		else return;
		event.preventDefault();
		selectTab(TABS[next].id);
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

	// A repo row starts with "follow global"; a repo may still name an agent
	// this machine does not have, so that value stays selectable too.
	function repoAgentOptions(agent: string) {
		const options = [{ value: '', label: t('settings.agentFollowGlobal') }, ...agentSelectOptions];
		if (agent && !options.some((option) => option.value === agent)) {
			options.push({ value: agent, label: agent });
		}
		return options;
	}

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

	function releaseValue(doc: Record<string, unknown>, name: string): ReleaseValue {
		const base = releaseDefaults();
		const entry = repoEntry(doc.repos, name);
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

	// draftFromConfig seeds a draft from the saved config: an empty agent,
	// model or language means the repo follows the global value and the key
	// stays out of the file.
	function draftFromConfig(doc: Record<string, unknown>, name: string): RepoDraft {
		const release = releaseValue(doc, name);
		return {
			agent: repoCodingAgent(doc.repos, name),
			model: repoString(doc.repos, name, 'model'),
			language: repoString(doc.repos, name, 'language'),
			releaseEnabled: release.enabled,
			releaseInterval: String(release.intervalDays),
			releaseTime: release.time
		};
	}

	function draftRelease(draft: RepoDraft): ReleaseValue {
		return {
			enabled: draft.releaseEnabled,
			intervalDays: Number(draft.releaseInterval),
			time: draft.releaseTime
		};
	}

	// draftIsCustom marks a repo that overrides at least one default, so the
	// project dropdown can show which projects carry their own settings.
	function draftIsCustom(draft: RepoDraft): boolean {
		return Boolean(
			draft.agent.trim() ||
				draft.model.trim() ||
				draft.language.trim() ||
				releaseIsSet(draftRelease(draft), releaseDefaults())
		);
	}

	let repoSelectOptions = $derived(
		repoNames.map((name) => {
			const draft = drafts[name];
			return {
				value: name,
				label: draft && draftIsCustom(draft) ? name + ' · ' + t('settings.repoCustom') : name
			};
		})
	);

	const LANGUAGE_OPTIONS = [
		{ value: '', label: 'settings.agentFollowGlobal' },
		{ value: 'auto', label: 'settings.languageAuto' },
		{ value: 'zh', label: 'settings.languageZH' },
		{ value: 'en', label: 'settings.languageEN' }
	] as const;

	let languageOptions = $derived(
		LANGUAGE_OPTIONS.map((option) => ({ value: option.value, label: t(option.label) }))
	);

	// applyRepoDrafts writes every repo's overrides and drops the keys that
	// follow the global value. A plain name list only turns into objects when
	// at least one repo actually overrides something, so an untouched config
	// keeps its compact shape.
	function applyRepoDrafts(doc: Record<string, unknown>, names: string[]): void {
		const defaults = releaseDefaults();
		const values = names.map(
			(name) => [name, drafts[name] ?? draftFromConfig(doc, name)] as const
		);
		const custom = values.some(([, draft]) => draftIsCustom(draft));
		if (custom) repoObjects(doc, names);
		// The three string fields share one writer so "follow global" always
		// deletes the key instead of pinning today's value.
		const byDraft = (field: 'agent' | 'model' | 'language') =>
			Object.fromEntries(values.map(([name, draft]) => [name, draft[field]]));
		setRepoStrings(doc, names, 'codingAgent', byDraft('agent'));
		setRepoStrings(doc, names, 'model', byDraft('model'));
		setRepoStrings(doc, names, 'language', byDraft('language'));
		for (const [name, draft] of values) {
			const entry = mutableRepoEntry(doc, name);
			if (!entry) continue;
			const release = draftRelease(draft);
			if (!releaseIsSet(release, defaults)) {
				delete entry.release;
				continue;
			}
			const interval = Number(release.intervalDays);
			const value: Record<string, unknown> = {
				enabled: Boolean(release.enabled),
				intervalDays: Number.isInteger(interval) && interval >= 1 ? interval : defaults.intervalDays
			};
			// The default time stays implicit; only a deliberate schedule is
			// written, so later default changes still reach these repos.
			if (release.time && release.time !== defaults.time) value.time = release.time;
			entry.release = value;
		}
	}

	// draftProblem returns the i18n key of the first invalid field, or an empty
	// string. The time input cannot produce a bad value, but a pasted or
	// restored draft can, and the PUT would reject it anyway.
	function draftProblem(names: string[]): string {
		for (const name of names) {
			const draft = drafts[name];
			if (!draft) continue;
			if (!Number.isInteger(Number(draft.releaseInterval)) || Number(draft.releaseInterval) < 1) {
				return 'settings.releaseIntervalInvalid';
			}
			if (draft.releaseTime && !RELEASE_TIME.test(draft.releaseTime)) {
				return 'settings.releaseTimeInvalid';
			}
		}
		return '';
	}

	// syncDrafts keeps edits already entered for repos that stay in the list,
	// seeds new names from the saved config and keeps the selected project
	// valid while the list is edited.
	function syncDrafts(): void {
		const names = parseRepoNames(repoText);
		const next: Record<string, RepoDraft> = {};
		for (const name of names) next[name] = drafts[name] ?? draftFromConfig(configDoc, name);
		drafts = next;
		if (!names.includes(selectedRepo)) selectedRepo = names[0] ?? '';
	}

	// The list and the text field share repoText; adding or removing a name
	// rebuilds it so saving keeps using the same parser and order.
	function addRepo(): void {
		const next = addRepoNames(repoText, repoDraft);
		repoDraft = '';
		if (next === repoNames.join('\n')) return;
		repoText = next;
		syncDrafts();
		// Show the project that was just added instead of the first one.
		const names = parseRepoNames(next);
		selectedRepo = names[names.length - 1] ?? selectedRepo;
	}

	function removeRepo(name: string): void {
		repoText = removeRepoName(repoText, name);
		syncDrafts();
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
		const names = repoList(configDoc);
		drafts = {};
		for (const name of names) drafts[name] = draftFromConfig(configDoc, name);
		selectedRepo = names[0] ?? '';
		// Open on the login tab while there is no usable config; everything
		// else starts on the config tab. A manual reload keeps the open tab.
		if (!tabInitialised) {
			tab = configProblem ? 'auth' : 'config';
			tabInitialised = true;
		}
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
		const problem = draftProblem(repos);
		if (problem) {
			formError(t(problem));
			return;
		}
		setRepoList(doc, repos);
		applyRepoDrafts(doc, repos);
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

		<div class="flex min-h-0 flex-1 flex-col md:flex-row">
			<!-- One tablist drives both layouts: a vertical column from md up, a
			     horizontal scrolling row below it. No focusgroup dependency:
			     roving tabindex plus the arrow keys works in every engine,
			     including the desktop shell's WKWebView. -->
			<div
				class="flex flex-none gap-1 overflow-x-auto border-b p-3 md:w-40 md:flex-col md:overflow-visible md:border-r md:border-b-0"
				role="tablist"
				aria-label={t('settings.aria')}
				data-testid="settings-tabs"
			>
				{#each TABS as section (section.id)}
					<button
						type="button"
						role="tab"
						id={'settings-tab-' + section.id}
						aria-selected={tab === section.id}
						tabindex={tab === section.id ? 0 : -1}
						class={cn(
							'rounded-md px-3 py-2 text-left text-sm whitespace-nowrap text-muted-foreground transition-colors hover:bg-accent hover:text-foreground',
							tab === section.id && 'bg-accent text-foreground'
						)}
						onclick={() => selectTab(section.id)}
						onkeydown={onTabKeydown}
					>
						{t(section.label)}
					</button>
				{/each}
			</div>

			<div class="min-h-0 flex-1 overflow-auto px-5 py-4">
				<!-- login -->
				<div
					id="settings-auth"
					role="tabpanel"
					aria-labelledby="settings-tab-auth"
					hidden={tab !== 'auth'}
					class="flex flex-col gap-2.5 rounded-lg border border-phase-work/30 bg-phase-work/5 p-4"
					data-testid="auth-panel"
				>
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
				</div>

				<div
					id="settings-repos"
					role="tabpanel"
					aria-labelledby="settings-tab-repos"
					hidden={tab !== 'repos'}
					class="flex flex-col gap-3"
					data-testid="repos-panel"
				>
					<h3 class="band-title mb-2">{t('settings.navRepos')}</h3>

					<!-- The global values every project inherits; editing them lives
					     in the config tab so there is only one source of truth. -->
					<div
						class="flex flex-wrap items-center gap-2 rounded-lg border bg-muted/40 px-3 py-2"
						data-testid="repos-defaults"
					>
						<div class="min-w-0 flex-1">
							<div class="text-xs tracking-[0.04em] text-muted-foreground">
								{t('settings.repoDefaults')}
							</div>
							<div class="truncate text-sm" data-testid="repos-defaults-value">
								{form.agent}
								{form.model ? ' · ' + form.model : ''}
								{form.proxy ? ' · ' + form.proxy : ''}
							</div>
						</div>
						<Button type="button" variant="ghost" size="sm" onclick={() => selectTab('config')}>
							{t('settings.repoDefaultsEdit')}
						</Button>
					</div>

					<div class="flex flex-col gap-1">
						<span class="text-xs tracking-[0.04em] text-muted-foreground">
							{t('settings.repoSelect')}
						</span>
						<div class="flex flex-wrap items-center gap-2">
							<Select
								options={repoSelectOptions}
								label={t('settings.repoSelect')}
								placeholder={t('settings.repoSelectPlaceholder')}
								testid="repo-select"
								class="min-w-[200px] flex-1"
								bind:value={selectedRepo}
							/>
							<Button
								type="button"
								variant="outline"
								class="flex-none text-muted-foreground hover:text-destructive"
								disabled={!selectedRepo}
								onclick={() => removeRepo(selectedRepo)}
								data-testid="repo-delete"
							>
								<Trash2 class="size-4" aria-hidden="true" />
								{t('settings.repoDelete')}
							</Button>
						</div>
					</div>

					{#if selectedRepo && drafts[selectedRepo]}
						<div
							class="flex flex-col rounded-lg border"
							data-testid="repo-card"
							data-repo={selectedRepo}
						>
							<div class="flex flex-wrap items-baseline gap-x-2 gap-y-0.5 border-b border-border/60 bg-muted/40 px-3 py-2">
								<span class="text-sm font-medium">{selectedRepo}</span>
								<span class="text-xs text-muted-foreground">· {t('settings.repoCardHint')}</span>
							</div>
							<div class="flex flex-col gap-3 p-3">
								<div class="flex flex-wrap gap-3">
									<div class="flex min-w-[150px] flex-1 flex-col gap-1">
										<span class="text-xs tracking-[0.04em] text-muted-foreground">
											{t('settings.agent')}
										</span>
										<Select
											options={repoAgentOptions(drafts[selectedRepo].agent)}
											label={t('settings.repoAgent', { repo: selectedRepo })}
											testid="repo-agent"
											bind:value={drafts[selectedRepo].agent}
										/>
									</div>
									<label class="flex min-w-[150px] flex-1 flex-col gap-1">
										<span class="text-xs tracking-[0.04em] text-muted-foreground">
											{t('settings.model')}
										</span>
										<Input
											list="repo-models"
											spellcheck="false"
											placeholder={form.model
												? t('settings.repoFollowWith', { value: form.model })
												: t('settings.repoModelHint')}
											data-testid="repo-model"
											bind:value={drafts[selectedRepo].model}
										/>
									</label>
									<div class="flex min-w-[140px] flex-1 flex-col gap-1">
										<span class="text-xs tracking-[0.04em] text-muted-foreground">
											{t('settings.language')}
										</span>
										<Select
											options={languageOptions}
											label={t('settings.language')}
											testid="repo-language"
											bind:value={drafts[selectedRepo].language}
										/>
									</div>
								</div>

								<div class="flex flex-col gap-2 border-t border-border/60 pt-3">
									<div class="flex flex-wrap items-baseline gap-2.5">
										<span class="text-xs tracking-[0.04em] text-muted-foreground">
											{t('settings.release')}
										</span>
										<small class="text-xs text-muted-foreground">{t('settings.releaseHint')}</small>
									</div>
									<div class="flex flex-wrap items-end gap-2.5">
										<label class="flex items-center gap-1.5 pb-1 text-2xs text-muted-foreground">
											<input
												type="checkbox"
												class="accent-phase-work"
												data-testid="repo-release-enabled"
												bind:checked={drafts[selectedRepo].releaseEnabled}
											/>
											{t('settings.releaseEnabled')}
										</label>
										<label class="flex flex-col gap-1 text-2xs tracking-[0.03em] text-muted-foreground">
											{t('settings.releaseInterval')}
											<input
												type="number"
												min="1"
												step="1"
												class="h-9 w-20 rounded-md border border-input bg-transparent px-2 text-sm"
												bind:value={drafts[selectedRepo].releaseInterval}
											/>
										</label>
										<label class="flex flex-col gap-1 text-2xs tracking-[0.03em] text-muted-foreground">
											{t('settings.releaseTime')}
											<input
												type="time"
												class="h-9 w-[7.5rem] rounded-md border border-input bg-transparent px-2 text-sm"
												bind:value={drafts[selectedRepo].releaseTime}
											/>
										</label>
									</div>
								</div>
							</div>
						</div>
					{:else}
						<p
							class="m-0 rounded-lg border border-dashed px-3 py-4 text-sm text-muted-foreground"
							data-testid="repos-empty"
						>
							{t('settings.reposEmpty')}
						</p>
					{/if}

					<div class="flex flex-col gap-1 border-t border-border/60 pt-3">
						<span class="text-xs tracking-[0.04em] text-muted-foreground">
							{t('settings.repoAddTitle')}
						</span>
						<div class="flex items-center gap-2">
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

					<datalist id="repo-models">
						{#each models as model (model)}
							<option value={model}></option>
						{/each}
					</datalist>
				</div>

				<div
					id="settings-config"
					role="tabpanel"
					aria-labelledby="settings-tab-config"
					hidden={tab !== 'config'}
					class="flex flex-col gap-3"
				>
					{#if configProblem}
						<div
							class="border-l-2 border-phase-work bg-phase-work/10 px-2.5 py-2 text-sm whitespace-pre-wrap"
							data-testid="settings-note"
						>
							{t('settings.setupHint')}
							{configProblem}
						</div>
					{/if}
					<h3 class="band-title mb-2">{t('settings.navConfig')}</h3>
					<form id="settings-form" class="flex flex-col gap-4" autocomplete="off" onsubmit={saveSettings}>

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
				</div>

				<div
					id="settings-update"
					role="tabpanel"
					aria-labelledby="settings-tab-update"
					hidden={tab !== 'update'}
					class="flex flex-col gap-3"
				>
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
				</div>
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

	/* The selected tab must not be signalled by colour alone (WCAG 1.4.1), so
	   it also carries a bar. The bar is a pseudo-element border, not a
	   background, so forced-colours mode keeps it and the tab never resizes. */
	[role='tab'] {
		position: relative;
	}

	[role='tab'][aria-selected='true']::after {
		content: '';
		position: absolute;
		inset: auto 0 0;
		border-block-end: 2px solid currentColor;
	}

	/* md and up: the tablist is a column, so the bar moves to the left edge. */
	@media (min-width: 48rem) {
		[role='tab'][aria-selected='true']::after {
			inset: 0 auto 0 0;
			border-block-end: 0;
			border-inline-start: 2px solid currentColor;
		}
	}
</style>
