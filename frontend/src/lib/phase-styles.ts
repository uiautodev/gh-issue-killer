import type { PhaseClass } from './phases';

export const phaseText: Record<PhaseClass, string> = {
	working: 'text-phase-work',
	waiting: 'text-phase-wait',
	blocked: 'text-phase-blocked',
	done: 'text-phase-done',
	idle: 'text-phase-idle'
};

export const phaseBg: Record<PhaseClass, string> = {
	working: 'bg-phase-work',
	waiting: 'bg-phase-wait',
	blocked: 'bg-phase-blocked',
	done: 'bg-phase-done',
	idle: 'bg-phase-idle'
};
