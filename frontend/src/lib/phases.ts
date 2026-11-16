export type PhaseClass = 'idle' | 'working' | 'waiting' | 'blocked' | 'done';

export interface PhaseMeta {
	key: string;
	cls: PhaseClass;
	step: number;
}

// Mirrors phaseNeedsUser in web.go: the ball is in the approver's court.
export const WAIT_PHASES = ['awaiting_approval', 'pr_open', 'blocked'];

export function needsMe(phase: string): boolean {
	return WAIT_PHASES.includes(phase);
}

export const PHASES: Record<string, PhaseMeta> = {
	new: { key: 'phase.new', cls: 'idle', step: 1 },
	'': { key: 'phase.new', cls: 'idle', step: 1 },
	planning: { key: 'phase.planning', cls: 'working', step: 1 },
	awaiting_approval: { key: 'phase.awaiting', cls: 'waiting', step: 2 },
	implementing: { key: 'phase.implementing', cls: 'working', step: 3 },
	blocked: { key: 'phase.blocked', cls: 'blocked', step: 3 },
	pr_open: { key: 'phase.prOpen', cls: 'waiting', step: 5 },
	merging: { key: 'phase.merging', cls: 'working', step: 6 },
	done: { key: 'phase.done', cls: 'done', step: 6 },
	cancelled: { key: 'phase.cancelled', cls: 'idle', step: 0 }
};

export function phaseMeta(phase: string): PhaseMeta {
	return PHASES[phase] || PHASES.new;
}
