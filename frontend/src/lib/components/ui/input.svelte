<script lang="ts">
import type { HTMLInputAttributes } from 'svelte/elements';
import { cn } from '#lib/utils.ts';

let { value = $bindable(), class: className, ...rest }: HTMLInputAttributes = $props();
</script>

<input
	class={cn(
		'flex h-7 w-full rounded-md border border-input bg-transparent px-2 py-1 text-[11px] shadow-xs transition-colors placeholder:text-muted-foreground focus-visible:border-ring disabled:cursor-not-allowed disabled:opacity-50',
		className
	)}
	bind:value
	{...rest}
/>
