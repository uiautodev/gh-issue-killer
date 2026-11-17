// The masthead's layout button mirrors shadcn-svelte's docs "Toggle layout":
// wide keeps the board's full 1600px shell, narrow centres it at 1100px so the
// same UI can be read in a quieter column. The choice lives on <html> so the
// CSS owns the width variable and components only use `.page-shell`.

export type LayoutWidth = 'wide' | 'narrow';

const LAYOUT_KEY = 'issuekiller-layout';

export const layout = $state<{ width: LayoutWidth }>({ width: 'wide' });

export function applyLayout(width: LayoutWidth): void {
	layout.width = width === 'narrow' ? 'narrow' : 'wide';
	try {
		localStorage.setItem(LAYOUT_KEY, layout.width);
	} catch {
		/* private mode */
	}
	document.documentElement.dataset.layout = layout.width;
}

export function toggleLayout(): void {
	applyLayout(layout.width === 'narrow' ? 'wide' : 'narrow');
}

export function initLayout(): void {
	let stored: LayoutWidth = 'wide';
	try {
		if (localStorage.getItem(LAYOUT_KEY) === 'narrow') stored = 'narrow';
	} catch {
		/* private mode */
	}
	applyLayout(stored);
}
