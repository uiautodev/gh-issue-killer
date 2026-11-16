<script lang="ts">
	import '../app.css';
	import { onMount } from 'svelte';
	import type { LayoutProps } from './$types';
	import { applyLanguage, storedLang } from '#lib/i18n.svelte.ts';
	import { initTheme } from '#lib/theme.svelte.ts';

	let { children }: LayoutProps = $props();

	onMount(() => {
		const stopTheme = initTheme();
		applyLanguage(storedLang());
		return stopTheme;
	});
</script>

<svelte:head>
	<link rel="icon" type="image/svg+xml" href="/logo.svg" />
</svelte:head>

{@render children()}
