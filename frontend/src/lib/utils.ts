import { clsx, type ClassValue } from 'clsx';
import { twMerge } from 'tailwind-merge';

export function cn(...inputs: ClassValue[]): string {
	return twMerge(clsx(inputs));
}

/** Human age for elapsed seconds, matching the old dashboard's compact units. */
export function humanAge(seconds: number | null | undefined): string {
	if (!seconds || seconds < 0) return '–';
	if (seconds < 60) return Math.round(seconds) + 's';
	if (seconds < 3600) return Math.floor(seconds / 60) + 'm';
	if (seconds < 86400) return Math.floor(seconds / 3600) + 'h';
	return Math.floor(seconds / 86400) + 'd';
}

/** Compact GitHub star count: exact below 1000, one decimal above ("9.3k"). */
export function formatStars(count: number): string {
	if (!Number.isFinite(count) || count <= 0) return '0';
	if (count < 1000) return String(Math.round(count));
	const thousands = count / 1000;
	if (thousands >= 100) return Math.round(thousands) + 'k';
	return thousands.toFixed(1).replace(/\.0$/, '') + 'k';
}

/** Only a positive waiting count is a call to action; zero stays quiet. */
export function waitingNeedsAttention(count: number | null | undefined): boolean {
	return typeof count === 'number' && Number.isFinite(count) && count > 0;
}

export function fmtTime(raw: string | null | undefined, lang: string): string {
	if (!raw) return '–';
	const date = new Date(raw);
	if (Number.isNaN(date.getTime())) return raw;
	return date.toLocaleString(lang === 'zh' ? 'zh-CN' : 'en-US', {
		hour12: false,
		month: '2-digit',
		day: '2-digit',
		hour: '2-digit',
		minute: '2-digit'
	});
}
