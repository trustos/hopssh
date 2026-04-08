<script lang="ts">
	import { onMount } from 'svelte';
	import { getTerminals } from '$lib/stores/terminals.svelte';
	import { connectShell, type ShellConnection } from '$lib/terminal/shell';

	const terms = getTerminals();

	// Non-reactive map — managed imperatively to avoid effect cycles.
	const connMap = new Map<string, ShellConnection>();
	let dragging = $state(false);
	let dragStartY = 0;
	let dragStartHeight = 0;

	// Connect/disconnect shells when sessions change.
	$effect(() => {
		const sessions = terms.sessions;
		const currentIds = new Set(sessions.map(s => s.id));

		// Close removed sessions.
		for (const [id, conn] of connMap) {
			if (!currentIds.has(id)) {
				conn.dispose();
				connMap.delete(id);
			}
		}

		// Connect new sessions.
		for (const session of sessions) {
			if (connMap.has(session.id)) continue;
			// Use requestAnimationFrame to ensure DOM element exists after render.
			const sid = session.id;
			const nid = session.networkId;
			const nodeId = session.nodeId;
			requestAnimationFrame(() => {
				if (connMap.has(sid)) return;
				const el = document.getElementById(`term-${sid}`);
				if (!el) return;
				const conn = connectShell(el as HTMLDivElement, nid, nodeId);
				connMap.set(sid, conn);
			});
		}
	});

	// Re-fit terminal when active tab changes (xterm needs correct dimensions).
	$effect(() => {
		const id = terms.activeId;
		if (!id) return;
		setTimeout(() => {
			const conn = connMap.get(id);
			if (conn) conn.fit();
		}, 50);
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
		class="flex flex-col border-t border-border bg-card shadow-[0_-1px_3px_0_rgba(0,0,0,0.2)]"
		style:height={terms.collapsed ? 'auto' : terms.maximized ? '100%' : `${terms.paneHeight}px`}
		style:min-height={terms.collapsed ? 'auto' : '150px'}
		style:flex-shrink="0"
	>
		<!-- Drag handle (only when not collapsed/maximized) -->
		{#if !terms.collapsed && !terms.maximized}
			<div
				class="group relative h-1.5 cursor-row-resize transition-colors"
				onpointerdown={startDrag}
				onpointermove={onDrag}
				onpointerup={endDrag}
				role="separator"
				aria-orientation="horizontal"
				aria-label="Resize terminal pane"
			>
				<!-- Visible line -->
				<div class="absolute inset-x-0 top-1/2 h-px -translate-y-1/2 bg-border group-hover:bg-primary/40 transition-colors"></div>
				<!-- Wider invisible hit area -->
				<div class="absolute inset-x-0 -top-1 -bottom-1"></div>
			</div>
		{/if}

		<!-- Tab bar -->
		<div class="flex items-center gap-1 border-b border-border/50 bg-background/80 backdrop-blur-sm px-3 py-1.5">
			{#each terms.sessions as session (session.id)}
				<div
					class="flex items-center gap-1.5 rounded-md px-2.5 py-1 text-xs font-medium transition-colors cursor-pointer {terms.activeId === session.id ? 'bg-primary/10 text-primary shadow-sm shadow-primary/5' : 'text-muted-foreground hover:text-foreground hover:bg-white/5'}"
					role="tab"
					aria-selected={terms.activeId === session.id}
				>
					<button
						onclick={() => { terms.focus(session.id); if (terms.collapsed) terms.toggleCollapse(); }}
						class="max-w-24 truncate font-mono"
					>{session.hostname}</button>
					<button
						onclick={() => terms.close(session.id)}
						class="ml-0.5 rounded-sm p-0.5 text-muted-foreground/40 hover:text-foreground hover:bg-white/10 transition-colors"
						aria-label="Close terminal"
					><svg class="h-3 w-3" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg></button>
				</div>
			{/each}

			<div class="flex-1"></div>

			<!-- Controls -->
			<button
				onclick={() => terms.toggleMaximize()}
				class="rounded-md p-1 text-muted-foreground hover:text-foreground hover:bg-white/5 transition-colors"
				title={terms.maximized ? 'Restore (Ctrl+Shift+Enter)' : 'Maximize (Ctrl+Shift+Enter)'}
				aria-label={terms.maximized ? 'Restore terminal' : 'Maximize terminal'}
			>
				{#if terms.maximized}
					<svg class="h-3.5 w-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="4 14 10 14 10 20"/><polyline points="20 10 14 10 14 4"/><line x1="14" y1="10" x2="21" y2="3"/><line x1="3" y1="21" x2="10" y2="14"/></svg>
				{:else}
					<svg class="h-3.5 w-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="15 3 21 3 21 9"/><polyline points="9 21 3 21 3 15"/><line x1="21" y1="3" x2="14" y2="10"/><line x1="3" y1="21" x2="10" y2="14"/></svg>
				{/if}
			</button>
			<button
				onclick={() => terms.toggleCollapse()}
				class="rounded-md p-1 text-muted-foreground hover:text-foreground hover:bg-white/5 transition-colors"
				title={terms.collapsed ? 'Show terminal' : 'Hide terminal'}
				aria-label={terms.collapsed ? 'Show terminal' : 'Hide terminal'}
			>
				{#if terms.collapsed}
					<svg class="h-3.5 w-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="18 15 12 9 6 15"/></svg>
				{:else}
					<svg class="h-3.5 w-3.5" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="6 9 12 15 18 9"/></svg>
				{/if}
			</button>
		</div>

		<!-- Terminal containers -->
		{#if !terms.collapsed}
			<div class="relative flex-1 overflow-hidden bg-[#0a0e14] rounded-t-lg">
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
