<script lang="ts">
	import { tick } from 'svelte';
	import Check from '@lucide/svelte/icons/check';
	import ChevronsUpDown from '@lucide/svelte/icons/chevrons-up-down';
	import { cn } from '#lib/utils.ts';

	export interface SelectOption {
		value: string;
		label: string;
	}

	// A local shadcn-svelte style select: a button trigger plus a listbox
	// popup. Keeping it dependency-free is deliberate (see AGENTS.md); focus
	// moves to the options themselves, so arrow keys, Home/End, Esc and focus
	// return all work without a roving tabindex.
	let {
		value = $bindable(''),
		options,
		placeholder = '',
		label,
		class: className,
		testid,
		disabled = false
	}: {
		value?: string;
		options: SelectOption[];
		placeholder?: string;
		label: string;
		class?: string;
		testid?: string;
		disabled?: boolean;
	} = $props();

	const listId = $props.id();

	let open = $state(false);
	let root = $state<HTMLDivElement | null>(null);
	let trigger = $state<HTMLButtonElement | null>(null);
	let list = $state<HTMLDivElement | null>(null);

	let selected = $derived(options.find((option) => option.value === value));
	let display = $derived(selected?.label ?? (value || placeholder));

	function focusOption(index: number): void {
		const nodes = list?.querySelectorAll<HTMLElement>('[role="option"]');
		if (!nodes?.length) return;
		const next = Math.max(0, Math.min(index, nodes.length - 1));
		nodes[next]?.focus();
	}

	function optionIndex(): number {
		const index = options.findIndex((option) => option.value === value);
		return index < 0 ? 0 : index;
	}

	function openMenu(): void {
		if (disabled || open) return;
		open = true;
		const index = optionIndex();
		tick().then(() => focusOption(index));
	}

	function closeMenu(refocus = true): void {
		if (!open) return;
		open = false;
		if (refocus) tick().then(() => trigger?.focus());
	}

	function choose(option: SelectOption): void {
		value = option.value;
		closeMenu();
	}

	function onTriggerKeydown(event: KeyboardEvent): void {
		if (event.key === 'ArrowDown' || event.key === 'ArrowUp') {
			event.preventDefault();
			if (open) focusOption(optionIndex());
			else openMenu();
		} else if (event.key === 'Escape') {
			closeMenu();
		}
	}

	function onOptionKeydown(event: KeyboardEvent, index: number): void {
		if (event.key === 'ArrowDown') {
			event.preventDefault();
			focusOption(index + 1);
		} else if (event.key === 'ArrowUp') {
			event.preventDefault();
			focusOption(index - 1);
		} else if (event.key === 'Home') {
			event.preventDefault();
			focusOption(0);
		} else if (event.key === 'End') {
			event.preventDefault();
			focusOption(options.length - 1);
		} else if (event.key === 'Escape') {
			event.preventDefault();
			closeMenu();
		} else if (event.key === 'Tab') {
			closeMenu(false);
		}
	}

	// pointerdown on the document catches a click on any other control before
	// that control receives focus, so the trigger never steals it back.
	$effect(() => {
		if (!open) return;
		function onPointerDown(event: PointerEvent): void {
			if (!root?.contains(event.target as Node)) closeMenu(false);
		}
		document.addEventListener('pointerdown', onPointerDown, true);
		return () => document.removeEventListener('pointerdown', onPointerDown, true);
	});
</script>

<div class={cn('relative', className)} bind:this={root}>
	<button
		type="button"
		class={cn(
			'flex h-9 w-full items-center justify-between gap-2 rounded-md border border-input bg-transparent px-2.5 text-sm whitespace-nowrap shadow-xs transition-colors hover:bg-accent/40 focus-visible:border-ring focus-visible:ring-2 focus-visible:ring-ring/30 disabled:pointer-events-none disabled:opacity-50',
			open && 'border-ring'
		)}
		bind:this={trigger}
		aria-haspopup="listbox"
		aria-expanded={open}
		aria-controls={listId}
		aria-label={label}
		data-testid={testid}
		{disabled}
		onclick={() => (open ? closeMenu() : openMenu())}
		onkeydown={onTriggerKeydown}
	>
		<span class={cn('truncate text-left', !selected && 'text-muted-foreground')}>
			{display}
		</span>
		<ChevronsUpDown class="size-4 flex-none opacity-50" aria-hidden="true" />
	</button>

	{#if open}
		<div
			id={listId}
			class="absolute top-full left-0 z-50 mt-1 max-h-64 w-full min-w-[10rem] overflow-auto rounded-md border bg-popover p-1 text-popover-foreground shadow-float"
			role="listbox"
			aria-label={label}
			bind:this={list}
		>
			{#each options as option, index (option.value)}
				<button
					type="button"
					role="option"
					tabindex="-1"
					aria-selected={option.value === value}
					class={cn(
						'flex w-full cursor-pointer items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm whitespace-nowrap outline-none hover:bg-accent focus:bg-accent focus:text-accent-foreground',
						option.value === value && 'bg-accent/60 font-medium'
					)}
					data-value={option.value}
					onclick={() => choose(option)}
					onkeydown={(event) => onOptionKeydown(event, index)}
				>
					<span class="min-w-0 flex-1 truncate">{option.label}</span>
					{#if option.value === value}
						<Check class="size-3.5 flex-none" aria-hidden="true" />
					{/if}
				</button>
			{/each}
		</div>
	{/if}
</div>
