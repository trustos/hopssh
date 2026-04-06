<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { connectShell, type ShellConnection } from '$lib/terminal/shell';
	import '@xterm/xterm/css/xterm.css';

	let terminalEl = $state<HTMLDivElement | null>(null);
	let shell = $state<ShellConnection | null>(null);

	const networkId = $derived(page.params.networkId);
	const nodeId = $derived(page.params.nodeId);

	$effect(() => {
		if (!terminalEl) return;

		shell = connectShell(terminalEl, networkId, nodeId);

		return () => {
			shell?.dispose();
			shell = null;
		};
	});
</script>

<div class="flex h-screen flex-col bg-[#0a0e14]">
	<!-- Minimal header -->
	<div class="flex items-center justify-between border-b border-white/10 px-4 py-2">
		<div class="flex items-center gap-3">
			<button
				onclick={() => goto(`/networks/${networkId}`)}
				class="text-sm text-white/60 hover:text-white"
			>
				← Back
			</button>
			<span class="text-sm font-mono text-white/80">{nodeId.slice(0, 8)}</span>
		</div>
		<div class="h-2 w-2 rounded-full bg-primary animate-hop-pulse"></div>
	</div>

	<!-- Terminal -->
	<div bind:this={terminalEl} class="flex-1"></div>
</div>
