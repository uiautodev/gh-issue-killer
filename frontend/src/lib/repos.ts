// Repos stay as a plain "owner/repo" list in the settings form; these helpers
// only normalise pasted input and add/remove one name, so the saved config
// keeps the exact order the user sees.

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
