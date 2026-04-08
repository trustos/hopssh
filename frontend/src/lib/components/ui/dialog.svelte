<script lang="ts">
	import { onMount } from 'svelte';

	let { open = false, onClose, children } = $props<{
		open: boolean;
		onClose: () => void;
		children: any;
	}>();

	let dialogEl = $state<HTMLDivElement | null>(null);
	let previousFocus = $state<HTMLElement | null>(null);

	$effect(() => {
		if (open) {
			previousFocus = document.activeElement as HTMLElement;
			// Focus first input in dialog after render
			requestAnimationFrame(() => {
				const firstInput = dialogEl?.querySelector('input, select, textarea, button[type="submit"]');
				if (firstInput instanceof HTMLElement) {
					firstInput.focus();
				}
			});
		} else if (previousFocus) {
			previousFocus.focus();
			previousFocus = null;
		}
	});

	function handleKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape' && open) {
			onClose();
		}
	}

	function handleBackdropClick(e: MouseEvent) {
		if (e.target === e.currentTarget) {
			onClose();
		}
	}
</script>

<svelte:window onkeydown={handleKeydown} />

{#if open}
	<!-- svelte-ignore a11y_click_events_have_key_events a11y_interactive_supports_focus -->
	<div
		class="fixed inset-0 z-50 flex items-center justify-center bg-black/50 transition-opacity"
		onclick={handleBackdropClick}
		role="dialog"
		aria-modal="true"
		tabindex="-1"
	>
		<!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
		<div
			bind:this={dialogEl}
			class="w-full max-w-md rounded-lg border bg-card p-6 shadow-lg"
			onclick={(e) => e.stopPropagation()}
		>
			{@render children()}
		</div>
	</div>
{/if}
