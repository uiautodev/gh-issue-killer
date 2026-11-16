import {
	getIssue,
	getLogs,
	getState,
	postAction
} from './api';
import { t } from './i18n.svelte';
import { needsMe, phaseMeta } from './phases';
import type { IssueState, Snapshot, Task } from './types';

export type Filter = 'all' | 'active' | 'wait' | 'blocked';

export interface Selection {
	repo: string;
	number: number;
}

interface Feedback {
	text: string;
	kind: 'idle' | 'ok' | 'fail';
}

// One store for the whole board: the masthead, lines, table, drawer, console
// and settings panel all read the same snapshot. Mutations stay here so the
// 2 s poll and the action buttons cannot drift apart.
class DashboardStore {
	data = $state<Snapshot | null>(null);
	filter = $state<Filter>('all');
	hideFinished = $state(false);
	repo = $state('');
	search = $state('');

	down = $state(false);
	live = $state(false);
	failures = $state(0);

	selected = $state<Selection | null>(null);
	selectedTask = $state<Task | null>(null);
	issue = $state<IssueState | null>(null);
	drawerError = $state('');
	actionBusy = $state(false);
	actionFeedback = $state<Feedback>({ text: '', kind: 'idle' });

	logText = $state('');
	logPaused = $state(false);
	logOpen = $state(true);

	settingsOpen = $state(false);
	setupShown = $state(false);

	private stateTimer: ReturnType<typeof setInterval> | null = null;
	private logTimer: ReturnType<typeof setInterval> | null = null;
	private stateInFlight = false;
	private logInFlight = false;

	tasks = $derived.by(() => {
		const data = this.data;
		if (!data) return [] as Task[];
		return data.tasks.filter((task) => this.matches(task));
	});

	counts = $derived.by(() => {
		const tasks = this.data?.tasks || [];
		const finished = tasks.filter(
			(task) => task.phase === 'done' || task.phase === 'cancelled'
		).length;
		return {
			all: this.hideFinished ? tasks.length - finished : tasks.length,
			active: tasks.filter((task) => task.active || task.queued).length,
			wait: tasks.filter((task) => needsMe(task.phase)).length,
			blocked: tasks.filter((task) => task.phase === 'blocked').length
		};
	});

	repoOptions = $derived.by(() => {
		const data = this.data;
		if (!data) return [] as { name: string; count: number }[];
		const counts = new Map<string, number>();
		for (const task of data.tasks) counts.set(task.repo, (counts.get(task.repo) || 0) + 1);
		const names = data.repos.map((repo) => repo.name);
		for (const name of counts.keys()) if (!names.includes(name)) names.push(name);
		return names.map((name) => ({ name, count: counts.get(name) || 0 }));
	});

	anyFiltered = $derived.by(() => (this.data?.tasks.length || 0) > 0);

	start(): () => void {
		this.refreshState();
		this.refreshLogs();
		this.stateTimer = setInterval(() => this.refreshState(), 2000);
		this.logTimer = setInterval(() => this.refreshLogs(), 3000);
		return () => this.stop();
	}

	stop(): void {
		if (this.stateTimer) clearInterval(this.stateTimer);
		if (this.logTimer) clearInterval(this.logTimer);
		this.stateTimer = null;
		this.logTimer = null;
	}

	phaseLabel(phase: string): string {
		const meta = phaseMeta(phase);
		return t(meta.key);
	}

	needsMe(task: Task): boolean {
		return needsMe(task.phase);
	}

	matches(task: Task): boolean {
		if (this.repo && task.repo !== this.repo) return false;
		if (this.hideFinished && (task.phase === 'done' || task.phase === 'cancelled')) return false;
		if (this.filter === 'active' && !(task.active || task.queued)) return false;
		if (this.filter === 'wait' && !needsMe(task.phase)) return false;
		if (this.filter === 'blocked' && task.phase !== 'blocked') return false;
		const needle = this.search.trim().toLowerCase();
		if (needle) {
			const hay = `${task.repo} ${task.number} ${task.title}`.toLowerCase();
			if (!hay.includes(needle)) return false;
		}
		return true;
	}

	async refreshState(): Promise<void> {
		if (this.stateInFlight) return;
		this.stateInFlight = true;
		try {
			const data = (await getState()) as Snapshot;
			this.failures = 0;
			this.down = false;
			this.live = true;
			this.data = data;
			if (this.repo && !this.repoOptions.some((option) => option.name === this.repo)) {
				this.repo = '';
			}
			if (data.configError && !this.settingsOpen && !this.setupShown) this.openSettings();

			if (this.selected) {
				const task = data.tasks.find(
					(item) => item.repo === this.selected?.repo && item.number === this.selected?.number
				);
				if (!task) {
					this.closeDrawer();
					return;
				}
				this.selectedTask = task;
				await this.refreshIssue();
			}
		} catch {
			this.failures++;
			this.down = true;
			this.live = false;
		} finally {
			this.stateInFlight = false;
		}
	}

	async refreshIssue(): Promise<void> {
		const selected = this.selected;
		if (!selected) return;
		try {
			this.issue = (await getIssue(selected.repo, selected.number)) as IssueState;
			this.drawerError = '';
		} catch (err) {
			this.issue = null;
			this.drawerError = err instanceof Error ? err.message : String(err);
		}
	}

	async selectTask(task: Task): Promise<void> {
		if (
			this.selected &&
			this.selected.repo === task.repo &&
			this.selected.number === task.number
		) {
			this.closeDrawer();
			return;
		}
		this.selected = { repo: task.repo, number: task.number };
		this.selectedTask = task;
		this.issue = null;
		this.drawerError = '';
		this.actionFeedback = { text: '', kind: 'idle' };
		await this.refreshIssue();
	}

	closeDrawer(): void {
		this.selected = null;
		this.selectedTask = null;
		this.issue = null;
		this.drawerError = '';
		this.actionFeedback = { text: '', kind: 'idle' };
	}

	async runAction(action: 'approve' | 'cancel'): Promise<void> {
		const task = this.selectedTask;
		if (!task || this.actionBusy) return;
		const question =
			action === 'approve' ? t('action.confirmApprove') : t('action.confirmCancel');
		if (!window.confirm(question)) return;
		this.actionBusy = true;
		this.actionFeedback = { text: t('action.submitting'), kind: 'idle' };
		try {
			await postAction(task.repo, task.number, action);
			this.actionFeedback = {
				text: action === 'approve' ? t('action.approved') : t('action.cancelled'),
				kind: 'ok'
			};
			setTimeout(() => this.refreshState(), 800);
		} catch (err) {
			this.actionFeedback = {
				text: t('action.failed', { msg: err instanceof Error ? err.message : String(err) }),
				kind: 'fail'
			};
		} finally {
			this.actionBusy = false;
		}
	}

	async refreshLogs(): Promise<void> {
		if (this.logPaused || !this.logOpen || this.logInFlight) return;
		this.logInFlight = true;
		try {
			this.logText = await getLogs(160);
		} catch {
			/* the state poll reports the connection problem */
		} finally {
			this.logInFlight = false;
		}
	}

	openSettings(): void {
		this.settingsOpen = true;
		this.setupShown = true;
	}

	closeSettings(): void {
		this.settingsOpen = false;
	}
}

export const dashboard = new DashboardStore();
