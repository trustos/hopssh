<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { networks as networksApi, nodes as nodesApi } from '$lib/api/client';
	import { ApiError } from '$lib/api/client';
	import type { NetworkDetailResponse, CreateNodeResponse } from '$lib/types/api';

	let network = $state<NetworkDetailResponse | null>(null);
	let loading = $state(true);
	let error = $state('');

	// Add Node dialog
	let showAddNode = $state(false);
	let addingNode = $state(false);
	let nodeResult = $state<CreateNodeResponse | null>(null);
	let addNodeError = $state('');
	let copied = $state(false);

	// Time ticker for reactive timeAgo
	let now = $state(Math.floor(Date.now() / 1000));

	const networkId = $derived(page.params.id);

	onMount(async () => {
		await loadNetwork();

		// Update "last seen" timestamps every 30 seconds.
		const interval = setInterval(() => {
			now = Math.floor(Date.now() / 1000);
		}, 30_000);
		return () => clearInterval(interval);
	});

	async function loadNetwork() {
		loading = true;
		error = '';
		try {
			network = await networksApi.get(networkId);
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to load network';
		} finally {
			loading = false;
		}
	}

	async function addNode() {
		addingNode = true;
		addNodeError = '';
		nodeResult = null;
		try {
			nodeResult = await nodesApi.create(networkId);
		} catch (e) {
			addNodeError = e instanceof ApiError ? e.message : 'Failed to create node';
		} finally {
			addingNode = false;
		}
	}

	function copyCommand() {
		if (!nodeResult) return;
		navigator.clipboard.writeText(nodeResult.installCommand);
		copied = true;
		setTimeout(() => (copied = false), 2000);
	}

	function closeAddNode() {
		showAddNode = false;
		nodeResult = null;
		addNodeError = '';
		if (nodeResult) loadNetwork(); // refresh list if a node was created
	}

	function statusColor(status: string) {
		switch (status) {
			case 'online':
				return 'bg-primary animate-hop-pulse';
			case 'enrolled':
				return 'bg-yellow-500';
			case 'offline':
				return 'bg-gray-500';
			default:
				return 'border border-dashed border-muted-foreground';
		}
	}

	function timeAgo(ts: number | null): string {
		if (!ts) return 'Never';
		const diff = now - ts;
		if (diff < 60) return 'Just now';
		if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
		if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
		return `${Math.floor(diff / 86400)}d ago`;
	}
</script>

<svelte:head>
	<title>{network?.name ?? 'Network'} - hopssh</title>
</svelte:head>

<div class="p-6">
	{#if loading}
		<div class="mb-6 h-8 w-48 animate-pulse rounded bg-muted"></div>
		<div class="space-y-3">
			{#each Array(3) as _}
				<div class="h-16 animate-pulse rounded-lg bg-muted"></div>
			{/each}
		</div>
	{:else if error}
		<div class="rounded-lg border border-destructive/50 bg-destructive/10 p-4 text-sm text-destructive">
			{error}
		</div>
	{:else if network}
		<div class="mb-6 flex items-center justify-between">
			<div>
				<h1 class="text-2xl font-semibold">{network.name}</h1>
				<p class="font-mono text-sm text-muted-foreground">{network.subnet}</p>
			</div>
			<button
				onclick={() => { showAddNode = true; addNode(); }}
				class="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90"
			>
				Add Node
			</button>
		</div>

		<!-- Add Node Dialog -->
		{#if showAddNode}
			<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
				<div class="w-full max-w-md rounded-lg border bg-card p-6 shadow-lg">
					<h2 class="mb-4 text-lg font-semibold">Add Node</h2>

					{#if addingNode}
						<div class="flex items-center gap-3 py-4">
							<div class="h-5 w-5 animate-spin rounded-full border-2 border-primary border-t-transparent"></div>
							<span class="text-sm text-muted-foreground">Generating enrollment token...</span>
						</div>
					{:else if addNodeError}
						<div class="mb-4 rounded-md bg-destructive/10 p-3 text-sm text-destructive">{addNodeError}</div>
					{:else if nodeResult}
						<div class="space-y-4">
							<div>
								<p class="mb-2 text-sm text-muted-foreground">Run this on your server:</p>
								<div class="relative">
									<pre class="overflow-x-auto rounded-md bg-muted p-3 font-mono text-xs">{nodeResult.installCommand}</pre>
									<button
										onclick={copyCommand}
										class="absolute right-2 top-2 rounded px-2 py-1 text-xs hover:bg-accent"
									>
										{copied ? 'Copied!' : 'Copy'}
									</button>
								</div>
							</div>
							<div class="text-xs text-muted-foreground">
								<p>Token expires in 10 minutes. Nebula IP: <span class="font-mono">{nodeResult.nebulaIP}</span></p>
							</div>
						</div>
					{/if}

					<div class="mt-4 flex justify-end">
						<button
							onclick={closeAddNode}
							class="rounded-md px-4 py-2 text-sm hover:bg-accent"
						>
							{nodeResult ? 'Done' : 'Cancel'}
						</button>
					</div>
				</div>
			</div>
		{/if}

		{#if network.nodes.length === 0}
			<div class="rounded-lg border border-dashed p-8 text-center">
				<p class="mb-2 text-lg font-medium">No nodes yet</p>
				<p class="text-sm text-muted-foreground">
					Add a node to get started. You'll get an enrollment command to run on your server.
				</p>
			</div>
		{:else}
			<div class="rounded-lg border">
				<table class="w-full text-sm">
					<thead>
						<tr class="border-b bg-muted/50">
							<th class="px-4 py-3 text-left font-medium">Status</th>
							<th class="px-4 py-3 text-left font-medium">Hostname</th>
							<th class="px-4 py-3 text-left font-medium">IP</th>
							<th class="px-4 py-3 text-left font-medium">OS</th>
							<th class="px-4 py-3 text-left font-medium">Last Seen</th>
							<th class="px-4 py-3 text-left font-medium">Actions</th>
						</tr>
					</thead>
					<tbody>
						{#each network.nodes as node}
							<tr class="border-b last:border-0 hover:bg-accent/50">
								<td class="px-4 py-3">
									<div class="flex items-center gap-2">
										<div class="h-2.5 w-2.5 rounded-full {statusColor(node.status)}"></div>
										<span class="text-xs capitalize text-muted-foreground">{node.status}</span>
									</div>
								</td>
								<td class="px-4 py-3">
									<a
										href="/terminal/{networkId}/{node.id}"
										class="font-mono font-medium text-primary hover:underline"
									>
										{node.hostname || node.id.slice(0, 8)}
									</a>
								</td>
								<td class="px-4 py-3 font-mono text-muted-foreground">{node.nebulaIP}</td>
								<td class="px-4 py-3 text-muted-foreground">
									{#if node.os}{node.os} {node.arch}{:else}<span class="text-muted-foreground/50">—</span>{/if}
								</td>
								<td class="px-4 py-3 text-muted-foreground">{timeAgo(node.lastSeenAt)}</td>
								<td class="px-4 py-3">
									<a
										href="/terminal/{networkId}/{node.id}"
										class="rounded px-2 py-1 text-xs font-medium text-primary hover:bg-primary/10"
									>
										Terminal
									</a>
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	{/if}
</div>
