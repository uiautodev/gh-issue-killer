<script lang="ts">
import type { HTMLTextareaAttributes } from 'svelte/elements';
import { cn } from '#lib/utils.ts';

let { value = $bindable(), class: className, ...rest }: HTMLTextareaAttributes = $props();
</script>

<textarea
	class={cn(
		'flex min-h-16 w-full rounded-md border border-input bg-transparent px-2 py-1.5 text-[11px] shadow-xs transition-colors placeholder:text-muted-foreground focus-visible:border-ring disabled:cursor-not-allowed disabled:opacity-50',
		className
	)}
	bind:value
	{...rest}
></textarea>
