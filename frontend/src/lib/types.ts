// Shapes returned by the Go dashboard API. Field names mirror the json tags in
// cmd/issuekiller/web.go, webconfig.go, webauth.go and state.go: the
// frontend never renames or reshapes a payload.

export interface Pool {
	plan: number;
	work: number;
}

export interface RepoInfo {
	name: string;
	agent: string;
	base: string;
	hookId: number;
	smee: string;
	lastPoll: string;
	queue: Pool;
	counts: Record<string, number>;
	issues: number;
	approvers: string[];
	worktreeDir: string;
}

export interface Task {
	repo: string;
	number: number;
	title: string;
	url: string;
	phase: string;
	phaseLabel: string;
	active: boolean;
	queued: boolean;
	startedAt: string;
	updatedAt: string;
	elapsedSec: number;
	prNumber: number;
	prUrl: string;
	blocked: string;
	checksTotal: number;
	checksOk: number;
	shots: number;
}

export interface Snapshot {
	time: string;
	version: string;
	platform: string;
	pid: number;
	uptimeSec: number;
	configFile: string;
	logPath: string;
	statePath: string;
	identity: string;
	webhook: boolean;
	embedded: boolean;
	configError: string;
	planLimit: number;
	workLimit: number;
	active: number;
	queued: number;
	waiting: number;
	repos: RepoInfo[];
	tasks: Task[];
}

export interface CheckResult {
	name: string;
	cmd: string;
	cwd: string;
	ok: boolean;
	code: number;
	timedOut?: boolean;
	durationSec?: number;
	outputTail?: string;
}

export interface Shot {
	name: string;
	caption: string;
	url: string;
	size?: number;
}

export interface IssueState {
	phase: string;
	plan?: string;
	sessionId?: string;
	extraNotes?: string;
	branch?: string;
	worktree?: string;
	prNumber?: number;
	prUrl?: string;
	checks?: CheckResult[];
	screenshots?: Shot[];
	blockedReason?: string;
	blockedByCI?: boolean;
	startedAt?: string;
	updatedAt?: string;
}

export interface DevicePayload {
	userCode: string;
	verificationUri: string;
	expiresIn: number;
	interval: number;
}

export interface AuthPayload {
	authenticated: boolean;
	login?: string;
	source?: string;
	expiresAt?: string;
	refreshable: boolean;
	pending?: boolean;
	device?: DevicePayload;
	error?: string;
}

export interface ConfigPayload {
	path: string;
	exists: boolean;
	config: Record<string, unknown>;
	defaults: Record<string, unknown>;
	version: string;
	platform: string;
	embedded: boolean;
	configError: string;
	error: string;
}

export interface DetectedAgent {
	id: string;
	name: string;
	binary: string;
	path: string;
	version: string;
}

export interface DetectedRepo {
	repo: string;
	permission: string;
	webhookReady: boolean;
	error?: string;
}

export interface DetectPayload {
	agents: DetectedAgent[];
	models: string[];
	proxy: string;
	repos: DetectedRepo[];
	error?: string;
}

export interface UpdatePayload {
	current: string;
	latest: string;
	available: boolean;
	url: string;
	notes: string;
	installed: boolean;
	error?: string;
}
