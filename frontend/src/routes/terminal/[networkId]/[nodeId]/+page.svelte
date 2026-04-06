<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { connectShell, type ShellConnection } from '$lib/terminal/shell';
	import '@xterm/xterm/css/xterm.css';

	let terminalEl = $state<HTMLDivElement | null>(null);
	let shell = $state<ShellConnection | null>(null);
	let status = $state<'connecting' | 'connected' | 'reconnecting' | 'failed' | 'ended'>('connecting');

	const networkId = $derived(page.params.networkId);
	const nodeId = $derived(page.params.nodeId);
	const hostname = $derived(
		page.url.searchParams.get('h') || nodeId.slice(0, 8)
	);

	$effect(() => {
		if (!terminalEl) return;

		status = 'connecting';
		const connection = connectShell(terminalEl, networkId, nodeId, () => {
			status = 'connected';
		});
		shell = connection;

		return () => {
			connection.dispose();
			shell = null;
		};
	});

	function handleReconnect() {
		if (shell) {
			status = 'reconnecting';
			shell.reconnect();
		}
	}
</script>

<svelte:head>
	<title>{hostname} - Terminal - hopssh</title>
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
			<span class="font-mono text-sm text-white/80">{hostname}</span>
		</div>
		<div class="flex items-center gap-2">
			{#if status === 'connecting' || status === 'reconnecting'}
				<span class="text-xs text-yellow-400">
					{status === 'connecting' ? 'Connecting...' : 'Reconnecting...'}
				</span>
				<div class="h-2 w-2 rounded-full bg-yellow-500 animate-pulse"></div>
			{:else if status === 'connected'}
				<div class="h-2 w-2 rounded-full bg-primary animate-hop-pulse"></div>
			{:else if status === 'failed' || status === 'ended'}
				<button
					onclick={handleReconnect}
					class="rounded bg-white/10 px-3 py-1 text-xs text-white hover:bg-white/20"
				>
					Reconnect
				</button>
				<button
					onclick={() => goto(`/networks/${networkId}`)}
					class="rounded bg-white/10 px-3 py-1 text-xs text-white/60 hover:bg-white/20"
				>
					Back to Network
				</button>
			{/if}
		</div>
	</div>

	<!-- Terminal -->
	<div bind:this={terminalEl} class="flex-1"></div>
</div>
