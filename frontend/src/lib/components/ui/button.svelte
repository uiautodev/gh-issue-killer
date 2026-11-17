<script lang="ts">
import type { Snippet } from 'svelte';
import type { HTMLButtonAttributes } from 'svelte/elements';
import { cn } from '#lib/utils.ts';

type Variant = 'default' | 'outline' | 'ghost' | 'secondary' | 'destructive';
type Size = 'default' | 'sm' | 'icon';

let {
	variant = 'outline',
	size = 'sm',
	class: className,
	children,
	...rest
}: HTMLButtonAttributes & { variant?: Variant; size?: Size; children?: Snippet } = $props();

const variants: Record<Variant, string> = {
	default: 'bg-primary text-primary-foreground hover:bg-primary/90',
	secondary: 'bg-secondary text-secondary-foreground hover:bg-secondary/80',
	outline: 'border border-input bg-background hover:bg-accent hover:text-accent-foreground',
	ghost: 'hover:bg-accent hover:text-accent-foreground',
	destructive: 'border border-destructive/60 text-destructive hover:bg-destructive/10'
};

const sizes: Record<Size, string> = {
	default: 'h-9 px-3.5 text-sm',
	sm: 'h-8 px-3 text-sm',
	icon: 'size-8 justify-center'
};
</script>

<button
	class={cn(
		'inline-flex items-center gap-1.5 rounded-md font-medium whitespace-nowrap transition-colors disabled:pointer-events-none disabled:opacity-50',
		variants[variant],
		sizes[size],
		className
	)}
	{...rest}
>
	{@render children?.()}
</button>
