// Repos stay as a plain "owner/repo" list in the settings form; these helpers
// normalise pasted input, add/remove one name and edit the per-repo overrides,
// so the saved config keeps the exact order the user sees.

/** One repo's override entry, whichever of the three `repos` shapes is used. */
export function repoEntry(repos: unknown, name: string): Record<string, unknown> | null {
	if (Array.isArray(repos)) {
		const found = repos.find(
			(item) => item && typeof item === 'object' && (item as { repo?: string }).repo === name
		);
		return found ? (found as Record<string, unknown>) : null;
	}
	if (repos && typeof repos === 'object') {
		const entry = (repos as Record<string, unknown>)[name];
		return entry && typeof entry === 'object' ? (entry as Record<string, unknown>) : null;
	}
	return null;
}

// repoObjects turns the array-of-names form into objects so a release block or
// a per-repo codingAgent can sit next to the repo. The keyed object form and
// the array-of-objects form are already per-repo.
export function repoObjects(doc: Record<string, unknown>, names: string[]): void {
	const repos = doc.repos;
	if (repos && !Array.isArray(repos) && typeof repos === 'object') return;
	const existing = new Map<string, Record<string, unknown>>();
	if (Array.isArray(repos)) {
		for (const item of repos) {
			if (item && typeof item === 'object' && typeof (item as { repo?: string }).repo === 'string') {
				existing.set((item as { repo: string }).repo, { ...(item as Record<string, unknown>) });
			} else if (typeof item === 'string') {
				existing.set(item, { repo: item });
			}
		}
	}
	doc.repos = names.map((name) => existing.get(name) || { repo: name });
}

/** The same entry as `repoEntry`, created on demand for the per-repo shapes. */
export function mutableRepoEntry(
	doc: Record<string, unknown>,
	name: string
): Record<string, unknown> | null {
	const repos = doc.repos;
	if (Array.isArray(repos)) {
		return (
			(repos.find(
				(item) => item && typeof item === 'object' && (item as { repo?: string }).repo === name
			) as Record<string, unknown> | undefined) || null
		);
	}
	if (repos && typeof repos === 'object') {
		const table = repos as Record<string, unknown>;
		if (!table[name] || typeof table[name] !== 'object') table[name] = {};
		return table[name] as Record<string, unknown>;
	}
	return null;
}

/** A repo's own value for one string key, or '' when it follows the global one. */
export function repoString(repos: unknown, name: string, key: string): string {
	const value = repoEntry(repos, name)?.[key];
	return typeof value === 'string' ? value.trim() : '';
}

/** A repo's own coding agent, or an empty string when it follows the global one. */
export function repoCodingAgent(repos: unknown, name: string): string {
	return repoString(repos, name, 'codingAgent');
}

// setRepoStrings writes one string field per repo and leaves the other
// overrides alone. An empty value means "follow global": the key is deleted so
// the repo keeps following the top-level setting. A plain name list only turns
// into objects when at least one repo overrides the field.
export function setRepoStrings(
	doc: Record<string, unknown>,
	names: string[],
	key: string,
	values: Record<string, string>
): void {
	const entries = names.map((name) => [name, (values[name] || '').trim()] as const);
	if (entries.some(([, value]) => value)) repoObjects(doc, names);
	for (const [name, value] of entries) {
		const entry = mutableRepoEntry(doc, name);
		if (!entry) continue;
		if (value) entry[key] = value;
		else delete entry[key];
	}
}

/** Split a pasted list into unique owner/repo names, accepting URLs and .git. */
export function parseRepoNames(text: string): string[] {
	return [
		...new Set(
			text
				.split(/[\s,;]+/)
				.map((value) => value.trim())
				.filter(Boolean)
				.map((value) =>
					value
						.replace(/^https?:\/\/github\.com\//i, '')
						.replace(/\.git$/i, '')
						.replace(/^\/+|\/+$/g, '')
				)
				.filter(Boolean)
		)
	];
}

/** Append new names to the list, dropping names that are already watched. */
export function addRepoNames(text: string, input: string): string {
	const names = parseRepoNames(text);
	const seen = new Set(names);
	for (const name of parseRepoNames(input)) {
		if (seen.has(name)) continue;
		seen.add(name);
		names.push(name);
	}
	return names.join('\n');
}

/** Remove one repository, leaving the order of the rest untouched. */
export function removeRepoName(text: string, name: string): string {
	return parseRepoNames(text)
		.filter((candidate) => candidate !== name)
		.join('\n');
}
