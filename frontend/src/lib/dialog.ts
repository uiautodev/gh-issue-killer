// Native `<dialog closedby="any">` is Chromium-only. WebKit (the desktop shell)
// still needs the outside-click fallback, so attach it only where the attribute
// is missing. Test the pointer position rather than event.target: the dialog is
// also the target for its own padding.
export function lightDismiss(dialog: HTMLDialogElement): () => void {
	if ('closedBy' in HTMLDialogElement.prototype) return () => {};

	function outside(event: PointerEvent): boolean {
		const rect = dialog.getBoundingClientRect();
		return (
			event.clientX < rect.left ||
			event.clientX > rect.right ||
			event.clientY < rect.top ||
			event.clientY > rect.bottom
		);
	}

	let pressedOutside = false;
	const onDown = (event: PointerEvent) => {
		pressedOutside = outside(event);
	};
	const onUp = (event: PointerEvent) => {
		if (pressedOutside && outside(event)) dialog.close();
		pressedOutside = false;
	};

	dialog.addEventListener('pointerdown', onDown);
	dialog.addEventListener('pointerup', onUp);
	return () => {
		dialog.removeEventListener('pointerdown', onDown);
		dialog.removeEventListener('pointerup', onUp);
	};
}

// showModalDialog opens a dialog exactly once and attaches the light-dismiss
// fallback for its lifetime.
export function showModalDialog(dialog: HTMLDialogElement): () => void {
	if (!dialog.open) dialog.showModal();
	return lightDismiss(dialog);
}
