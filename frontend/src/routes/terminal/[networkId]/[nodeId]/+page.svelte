<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { connectShell, type ShellConnection } from '$lib/terminal/shell';
	import '@xterm/xterm/css/xterm.css';

	let terminalEl = $state<HTMLDivElement | null>(null);
	let shell = $state<ShellConnection | null>(null);
	let connected = $state(false);

	const networkId = $derived(page.params.networkId);
	const nodeId = $derived(page.params.nodeId);

	$effect(() => {
		if (!terminalEl) return;

		const connection = connectShell(terminalEl, networkId, nodeId, () => {
			connected = true;
		});
		shell = connection;

		return () => {
			connection.dispose();
			shell = null;
			connected = false;
		};
	});
</script>

<svelte:head>
	<title>{nodeId.slice(0, 8)} - Terminal - hopssh</title>
</svelte:head>

<div class="flex h-screen flex-col bg-[#0a0e14]">
	<!-- Header -->
	<div class="flex items-center justify-between border-b border-white/10 px-4 py-2">
		<div class="flex items-center gap-3">
			<button
				onclick={() => goto(`/networks/${networkId}`)}
				class="text-sm text-white/60 hover:text-white"
				aria-label="Back to network"
			>
				← Back
			</button>
			<span class="font-mono text-sm text-white/80">{nodeId.slice(0, 8)}</span>
		</div>
		<div class="flex items-center gap-2">
			{#if !connected}
				<span class="text-xs text-white/40">Connecting...</span>
			{/if}
			<div
				class="h-2 w-2 rounded-full {connected ? 'bg-primary animate-hop-pulse' : 'bg-yellow-500'}"
			></div>
		</div>
	</div>

	<!-- Terminal -->
	<div bind:this={terminalEl} class="flex-1"></div>
</div>
