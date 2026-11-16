// Thin wrappers around the dashboard API. Every mutation posts JSON and keeps
// the loopback Origin check in web.go happy; errors are normalised to the
// server's {"error": "..."} message.

async function errorMessage(response: Response): Promise<string> {
	try {
		const data = await response.json();
		if (data && typeof data.error === 'string' && data.error) return data.error;
	} catch {
		/* not JSON */
	}
	return 'HTTP ' + response.status;
}

export async function getState() {
	const response = await fetch('/api/state', { cache: 'no-store' });
	if (!response.ok) throw new Error('HTTP ' + response.status);
	return response.json();
}

export async function getIssue(repo: string, number: number) {
	const response = await fetch(
		`/api/issue?repo=${encodeURIComponent(repo)}&number=${number}`,
		{ cache: 'no-store' }
	);
	if (!response.ok) throw new Error('HTTP ' + response.status);
	return response.json();
}

export async function getLogs(lines = 160) {
	const response = await fetch(`/api/logs?lines=${lines}`, { cache: 'no-store' });
	if (!response.ok) throw new Error('HTTP ' + response.status);
	return response.text();
}

export async function postAction(repo: string, number: number, action: 'approve' | 'cancel') {
	const response = await fetch('/api/action', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ repo, number, action })
	});
	if (!response.ok) throw new Error(await errorMessage(response));
	return response.json();
}

export async function getConfig() {
	const response = await fetch('/api/config', { cache: 'no-store' });
	if (!response.ok) throw new Error(await errorMessage(response));
	return response.json();
}

export async function putConfig(config: Record<string, unknown>) {
	const response = await fetch('/api/config', {
		method: 'PUT',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ config })
	});
	if (!response.ok) throw new Error(await errorMessage(response));
	return response.json();
}

export async function getDetect(params: URLSearchParams) {
	const response = await fetch('/api/detect?' + params.toString(), { cache: 'no-store' });
	if (!response.ok) throw new Error(await errorMessage(response));
	return response.json();
}

export async function postRestart() {
	const response = await fetch('/api/restart', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: '{}'
	});
	if (!response.ok) throw new Error(await errorMessage(response));
	return response.json();
}

export async function getUpdate() {
	const response = await fetch('/api/update', { cache: 'no-store' });
	if (!response.ok) throw new Error(await errorMessage(response));
	return response.json();
}

export async function postUpdate() {
	const response = await fetch('/api/update', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: '{}'
	});
	if (!response.ok) throw new Error(await errorMessage(response));
	const data = await response.json();
	if (data.error) throw new Error(data.error);
	return data;
}

export async function getAuth() {
	const response = await fetch('/api/auth', { cache: 'no-store' });
	if (!response.ok) throw new Error(await errorMessage(response));
	return response.json();
}

export async function startDevice() {
	const response = await fetch('/api/auth/device', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: '{}'
	});
	if (!response.ok) throw new Error(await errorMessage(response));
	return response.json();
}

export async function pollDevice() {
	const response = await fetch('/api/auth/device', { cache: 'no-store' });
	if (!response.ok) throw new Error(await errorMessage(response));
	return response.json();
}

export async function postToken(token: string) {
	const response = await fetch('/api/auth/token', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ token })
	});
	if (!response.ok) throw new Error(await errorMessage(response));
	return response.json();
}

// The desktop shell loads the page with ?surface=desktop. Its WKWebView drops
// target="_blank" clicks, so a link is posted to the engine, which opens the
// system browser. A normal browser keeps window.open.
export function openExternal(url: string): void {
	if (!/^https?:\/\//i.test(url)) return;
	const desktopSurface = new URLSearchParams(location.search).get('surface') === 'desktop';
	if (!desktopSurface) {
		window.open(url, '_blank', 'noopener');
		return;
	}
	fetch('/api/open-url', {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify({ url })
	}).catch(() => {});
}
