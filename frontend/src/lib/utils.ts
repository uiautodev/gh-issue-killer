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
