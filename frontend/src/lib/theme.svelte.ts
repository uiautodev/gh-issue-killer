// Theme modes mirror the old dashboard: auto follows the OS, light/dark force
// a side. shadcn-svelte tokens switch on the `.dark` class, so `auto` resolves
// through matchMedia and keeps listening for OS changes.

export type ThemeMode = 'auto' | 'light' | 'dark';

const THEME_KEY = 'ghik-theme';

function systemPrefersDark(): boolean {
	if (typeof window === 'undefined' || !window.matchMedia) return true;
	return window.matchMedia('(prefers-color-scheme: dark)').matches;
}

export const theme = $state<{ mode: ThemeMode; systemDark: boolean }>({
	mode: 'auto',
	systemDark: true
});

export function isDark(): boolean {
	return theme.mode === 'dark' || (theme.mode === 'auto' && theme.systemDark);
}

export function applyTheme(mode: ThemeMode): void {
	theme.mode = mode === 'light' || mode === 'dark' ? mode : 'auto';
	try {
		localStorage.setItem(THEME_KEY, theme.mode);
	} catch {
		/* private mode */
	}
	document.documentElement.dataset.theme = theme.mode;
	document.documentElement.classList.toggle('dark', isDark());
}

// initTheme reads the stored mode and returns a cleanup for the media listener.
export function initTheme(): () => void {
	let stored: ThemeMode = 'auto';
	try {
		const raw = localStorage.getItem(THEME_KEY);
		if (raw === 'light' || raw === 'dark' || raw === 'auto') stored = raw;
	} catch {
		/* private mode */
	}
	theme.systemDark = systemPrefersDark();
	applyTheme(stored);

	if (typeof window === 'undefined' || !window.matchMedia) return () => {};
	const query = window.matchMedia('(prefers-color-scheme: dark)');
	const onChange = (event: MediaQueryListEvent) => {
		theme.systemDark = event.matches;
		document.documentElement.classList.toggle('dark', isDark());
	};
	query.addEventListener('change', onChange);
	return () => query.removeEventListener('change', onChange);
}
