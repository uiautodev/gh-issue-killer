import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';
import tailwindcss from '@tailwindcss/vite';
import { fileURLToPath } from 'node:url';

// The dashboard ships inside the Go binary, so the static build goes straight
// into the embed directory instead of a separate `build/` folder. SPA mode
// (index.html fallback) keeps deep links working through web.go's fallback.
const dist = fileURLToPath(new URL('../cmd/gh-issue-killer/web/dist', import.meta.url));

export default defineConfig({
	plugins: [
		tailwindcss(),
		sveltekit({
			appDir: 'assets',
			compilerOptions: {
				// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
				runes: ({ filename }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},

			adapter: adapter({
				pages: dist,
				assets: dist,
				fallback: 'index.html',
				// Routes are all client-side; nothing is prerendered.
				strict: false
			})
		})
	]
});
