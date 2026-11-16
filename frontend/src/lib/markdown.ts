// Minimal Markdown renderer for the plan block. It mirrors the old dashboard's
// renderer (headings, lists, tables, quotes, fences, inline code/bold/italic/
// links) but builds a sanitized HTML string: every text node is escaped and
// links are limited to http(s), because plan text comes from GitHub.

const ESCAPES: Record<string, string> = {
	'&': '&amp;',
	'<': '&lt;',
	'>': '&gt;',
	'"': '&quot;',
	"'": '&#39;'
};

function escapeHtml(value: string): string {
	return value.replace(/[&<>"']/g, (char) => ESCAPES[char]);
}

function inline(source: string): string {
	const tokens = /(`[^`]+`|\*\*[^*]+\*\*|\*[^*]+\*|\[[^\]]+\]\([^)]+\))/g;
	let out = '';
	let last = 0;
	let match: RegExpExecArray | null;
	while ((match = tokens.exec(source))) {
		if (match.index > last) out += escapeHtml(source.slice(last, match.index));
		const token = match[0];
		if (token.startsWith('`')) {
			out += '<code>' + escapeHtml(token.slice(1, -1)) + '</code>';
		} else if (token.startsWith('**')) {
			out += '<strong>' + escapeHtml(token.slice(2, -2)) + '</strong>';
		} else if (token.startsWith('*')) {
			out += '<em>' + escapeHtml(token.slice(1, -1)) + '</em>';
		} else {
			const link = token.match(/^\[([^\]]+)\]\(([^)]+)\)$/);
			if (link && /^https?:\/\//i.test(link[2])) {
				out +=
					'<a href="' +
					escapeHtml(link[2]) +
					'" target="_blank" rel="noreferrer">' +
					escapeHtml(link[1]) +
					'</a>';
			} else {
				out += escapeHtml(token);
			}
		}
		last = tokens.lastIndex;
	}
	if (last < source.length) out += escapeHtml(source.slice(last));
	return out;
}

function splitRow(line: string): string[] {
	return line
		.trim()
		.replace(/^\|/, '')
		.replace(/\|$/, '')
		.split('|')
		.map((cell) => cell.trim());
}

function startsBlock(line: string): boolean {
	return (
		/^\s*```/.test(line) ||
		/^#{1,6}\s+/.test(line) ||
		/^\s*([-*+]|\d+\.)\s+/.test(line) ||
		/^\s*>/.test(line) ||
		/^\s*(---|\*\*\*|___)\s*$/.test(line) ||
		/^\s*\|/.test(line)
	);
}

export function renderMarkdown(source: string | null | undefined): string {
	const lines = String(source || '')
		.replace(/\r\n/g, '\n')
		.split('\n');
	let out = '';
	let i = 0;

	while (i < lines.length) {
		const line = lines[i];
		if (!line.trim()) {
			i++;
			continue;
		}

		const fence = line.match(/^\s*```(\S*)\s*$/);
		if (fence) {
			const body: string[] = [];
			i++;
			while (i < lines.length && !/^\s*```\s*$/.test(lines[i])) body.push(lines[i++]);
			i++;
			out += '<pre><code>' + escapeHtml(body.join('\n')) + '</code></pre>';
			continue;
		}

		const heading = line.match(/^(#{1,6})\s+(.*)$/);
		if (heading) {
			const level = Math.min(heading[1].length + 1, 6);
			out += '<h' + level + '>' + inline(heading[2]) + '</h' + level + '>';
			i++;
			continue;
		}

		if (/^\s*(---|\*\*\*|___)\s*$/.test(line)) {
			out += '<hr />';
			i++;
			continue;
		}

		if (/^\s*>/.test(line)) {
			const body: string[] = [];
			while (i < lines.length && (/^\s*>/.test(lines[i]) || !lines[i].trim())) {
				body.push(lines[i].replace(/^\s*>\s?/, ''));
				i++;
			}
			out += '<blockquote>' + inline(body.join(' ').trim()) + '</blockquote>';
			continue;
		}

		if (/^\s*([-*+])\s+/.test(line)) {
			out += '<ul>';
			while (i < lines.length && /^\s*([-*+])\s+/.test(lines[i])) {
				out += '<li>' + inline(lines[i].replace(/^\s*[-*+]\s+/, '')) + '</li>';
				i++;
			}
			out += '</ul>';
			continue;
		}

		if (/^\s*\d+\.\s+/.test(line)) {
			out += '<ol>';
			while (i < lines.length && /^\s*\d+\.\s+/.test(lines[i])) {
				out += '<li>' + inline(lines[i].replace(/^\s*\d+\.\s+/, '')) + '</li>';
				i++;
			}
			out += '</ol>';
			continue;
		}

		if (/^\s*\|/.test(line) && i + 1 < lines.length && /^\s*\|?[\s:|-]+\|/.test(lines[i + 1])) {
			const header = splitRow(lines[i]);
			i += 2;
			out += '<table><thead><tr>';
			for (const cell of header) out += '<th>' + inline(cell) + '</th>';
			out += '</tr></thead><tbody>';
			while (i < lines.length && /^\s*\|/.test(lines[i])) {
				out += '<tr>';
				for (const cell of splitRow(lines[i])) out += '<td>' + inline(cell) + '</td>';
				out += '</tr>';
				i++;
			}
			out += '</tbody></table>';
			continue;
		}

		const para: string[] = [];
		while (i < lines.length && lines[i].trim() && !startsBlock(lines[i])) {
			para.push(lines[i].trim());
			i++;
		}
		out += '<p>' + inline(para.join(' ')) + '</p>';
	}

	return out;
}
