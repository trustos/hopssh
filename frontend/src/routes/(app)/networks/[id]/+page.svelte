<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { networks as networksApi } from '$lib/api/client';
	import type { NetworkDetailResponse, NodeResponse } from '$lib/types/api';

	let network = $state<NetworkDetailResponse | null>(null);
	let loading = $state(true);
	let error = $state('');

	const networkId = $derived(page.params.id);

	onMount(async () => {
		try {
			network = await networksApi.get(networkId);
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to load network';
		} finally {
			loading = false;
		}
	});

	function statusColor(status: string) {
		switch (status) {
			case 'online': return 'bg-primary animate-hop-pulse';
			case 'enrolled': return 'bg-yellow-500';
			case 'offline': return 'bg-gray-500';
			default: return 'border border-dashed border-muted-foreground';
		}
	}

	function timeAgo(ts: number | null): string {
		if (!ts) return 'Never';
		const diff = Math.floor(Date.now() / 1000 - ts);
		if (diff < 60) return 'Just now';
		if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
		if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
		return `${Math.floor(diff / 86400)}d ago`;
	}
</script>

<div class="p-6">
	{#if loading}
		<div class="h-8 w-48 animate-pulse rounded bg-muted mb-6"></div>
		<div class="space-y-3">
			{#each [1, 2] as _}
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
				<p class="text-sm text-muted-foreground font-mono">{network.subnet}</p>
			</div>
			<button class="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90">
				Add Node
			</button>
		</div>

		{#if network.nodes.length === 0}
			<div class="rounded-lg border border-dashed p-8 text-center">
				<p class="mb-2 text-lg font-medium">No nodes yet</p>
				<p class="text-sm text-muted-foreground">Add a node to get started. You'll get an enrollment command to run on your server.</p>
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
										<span class="text-xs text-muted-foreground capitalize">{node.status}</span>
									</div>
								</td>
								<td class="px-4 py-3">
									<a
										href="/terminal/{networkId}/{node.id}"
										class="font-medium text-primary hover:underline font-mono"
									>
										{node.hostname || node.id.slice(0, 8)}
									</a>
								</td>
								<td class="px-4 py-3 font-mono text-muted-foreground">{node.nebulaIP}</td>
								<td class="px-4 py-3 text-muted-foreground">{node.os} {node.arch}</td>
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
