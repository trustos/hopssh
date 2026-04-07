<script lang="ts">
	import { onMount } from 'svelte';
	import { getTerminals } from '$lib/stores/terminals.svelte';
	import { connectShell, type ShellConnection } from '$lib/terminal/shell';

	const terms = getTerminals();

	// Track live shell connections keyed by session ID.
	let connections = $state<Map<string, ShellConnection>>(new Map());
	let containerEls = $state<Map<string, HTMLDivElement>>(new Map());
	let dragging = $state(false);
	let dragStartY = 0;
	let dragStartHeight = 0;

	// Connect/disconnect shells when sessions change.
	$effect(() => {
		const currentIds = new Set(terms.sessions.map(s => s.id));

		// Close removed sessions.
		for (const [id, conn] of connections) {
			if (!currentIds.has(id)) {
				conn.dispose();
				connections.delete(id);
			}
		}

		// Connect new sessions.
		for (const session of terms.sessions) {
			if (connections.has(session.id)) continue;
			const el = document.getElementById(`term-${session.id}`);
			if (!el) continue;
			const conn = connectShell(el as HTMLDivElement, session.networkId, session.nodeId);
			connections.set(session.id, conn);
		}

		connections = new Map(connections);
	});

	function startDrag(e: PointerEvent) {
		dragging = true;
		dragStartY = e.clientY;
		dragStartHeight = terms.paneHeight;
		(e.target as HTMLElement).setPointerCapture(e.pointerId);
	}

	function onDrag(e: PointerEvent) {
		if (!dragging) return;
		const delta = dragStartY - e.clientY;
		terms.setHeight(dragStartHeight + delta);
	}

	function endDrag() {
		dragging = false;
	}

	function handleKeydown(e: KeyboardEvent) {
		if (e.ctrlKey && e.shiftKey && e.key === 'Enter') {
			e.preventDefault();
			terms.toggleMaximize();
		}
	}

	onMount(() => {
		window.addEventListener('keydown', handleKeydown);
		return () => window.removeEventListener('keydown', handleKeydown);
	});
</script>

{#if terms.hasTerminals}
	<div
		class="flex flex-col border-t border-border bg-card"
		style:height={terms.collapsed ? 'auto' : terms.maximized ? '100%' : `${terms.paneHeight}px`}
		style:min-height={terms.collapsed ? 'auto' : '150px'}
		style:flex-shrink="0"
	>
		<!-- Drag handle (only when not collapsed/maximized) -->
		{#if !terms.collapsed && !terms.maximized}
			<div
				class="h-1 cursor-row-resize bg-border hover:bg-primary/30 transition-colors"
				onpointerdown={startDrag}
				onpointermove={onDrag}
				onpointerup={endDrag}
				role="separator"
				aria-orientation="horizontal"
			></div>
		{/if}

		<!-- Tab bar -->
		<div class="flex items-center gap-0.5 border-b border-border bg-background px-2 py-1">
			{#each terms.sessions as session (session.id)}
				<div
					class="flex items-center gap-1 rounded px-2 py-0.5 text-xs transition-colors cursor-pointer {terms.activeId === session.id ? 'bg-primary/15 text-primary' : 'text-muted-foreground hover:text-foreground'}"
					role="tab"
					aria-selected={terms.activeId === session.id}
				>
					<button
						onclick={() => { terms.focus(session.id); if (terms.collapsed) terms.toggleCollapse(); }}
						class="max-w-24 truncate font-mono"
					>{session.hostname}</button>
					<button
						onclick={() => terms.close(session.id)}
						class="ml-0.5 rounded text-muted-foreground/40 hover:text-foreground"
						aria-label="Close terminal"
					>×</button>
				</div>
			{/each}

			<div class="flex-1"></div>

			<!-- Controls -->
			<button
				onclick={() => terms.toggleMaximize()}
				class="rounded px-1.5 py-0.5 text-xs text-muted-foreground hover:text-foreground"
				title={terms.maximized ? 'Restore (Ctrl+Shift+Enter)' : 'Maximize (Ctrl+Shift+Enter)'}
			>
				{terms.maximized ? '↓' : '↑'}
			</button>
			<button
				onclick={() => terms.toggleCollapse()}
				class="rounded px-1.5 py-0.5 text-xs text-muted-foreground hover:text-foreground"
				title={terms.collapsed ? 'Show terminal' : 'Hide terminal'}
			>
				{terms.collapsed ? '▲' : '▼'}
			</button>
		</div>

		<!-- Terminal containers -->
		{#if !terms.collapsed}
			<div class="relative flex-1 overflow-hidden bg-[#0a0e14]">
				{#each terms.sessions as session, i (session.id)}
					{@const isActive = terms.activeId === session.id}
					<div
						class="absolute inset-0"
						style:display={isActive ? 'block' : 'none'}
						id="term-{session.id}"
					></div>
				{/each}
			</div>
		{/if}
	</div>
{/if}
