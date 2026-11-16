<script lang="ts">
	import { onMount } from 'svelte';
	import { dashboard } from '#lib/dashboard.svelte.ts';
	import ConsolePanel from '#lib/components/console-panel.svelte';
	import LinesBand from '#lib/components/lines-band.svelte';
	import Masthead from '#lib/components/masthead.svelte';
	import SettingsPanel from '#lib/components/settings-panel.svelte';
	import TaskDrawer from '#lib/components/task-drawer.svelte';
	import TaskTable from '#lib/components/task-table.svelte';

	function toggleSettings(): void {
		if (dashboard.settingsOpen) dashboard.closeSettings();
		else dashboard.openSettings();
	}

	onMount(() => {
		const stopPolling = dashboard.start();
		const onKey = (event: KeyboardEvent) => {
			if (event.key !== 'Escape') return;
			dashboard.closeDrawer();
			dashboard.closeSettings();
		};
		window.addEventListener('keydown', onKey);
		return () => {
			stopPolling();
			window.removeEventListener('keydown', onKey);
		};
	});
</script>

<div class="flex min-h-screen flex-col">
	<Masthead onOpenSettings={toggleSettings} />
	<LinesBand
		repos={dashboard.data?.repos ?? []}
		planLimit={dashboard.data?.planLimit ?? 1}
		workLimit={dashboard.data?.workLimit ?? 1}
	/>
	<TaskTable />
	<ConsolePanel />
</div>

{#if dashboard.settingsOpen}
	<SettingsPanel />
{/if}

<TaskDrawer />
