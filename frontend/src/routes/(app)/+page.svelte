<script lang="ts">
	import { onMount } from 'svelte';
	import { networks as networksApi } from '$lib/api/client';
	import type { NetworkResponse } from '$lib/types/api';

	let networkList = $state<NetworkResponse[]>([]);
	let loading = $state(true);
	let error = $state('');

	onMount(async () => {
		try {
			networkList = await networksApi.list();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to load networks';
		} finally {
			loading = false;
		}
	});
</script>

<div class="p-6">
	<div class="mb-6 flex items-center justify-between">
		<h1 class="text-2xl font-semibold">Networks</h1>
		<button class="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90">
			Create Network
		</button>
	</div>

	{#if loading}
		<div class="space-y-3">
			{#each [1, 2, 3] as _}
				<div class="h-16 animate-pulse rounded-lg bg-muted"></div>
			{/each}
		</div>
	{:else if error}
		<div class="rounded-lg border border-destructive/50 bg-destructive/10 p-4 text-sm text-destructive">
			{error}
		</div>
	{:else if networkList.length === 0}
		<div class="rounded-lg border border-dashed p-8 text-center">
			<p class="mb-2 text-lg font-medium">No networks yet</p>
			<p class="text-sm text-muted-foreground">Create your first network to get started.</p>
		</div>
	{:else}
		<div class="space-y-3">
			{#each networkList as network}
				<a
					href="/networks/{network.id}"
					class="flex items-center justify-between rounded-lg border p-4 transition-colors hover:bg-accent"
				>
					<div>
						<div class="font-medium">{network.name}</div>
						<div class="text-sm text-muted-foreground font-mono">{network.subnet}</div>
					</div>
					<div class="text-sm text-muted-foreground">
						{network.nodeCount} {network.nodeCount === 1 ? 'node' : 'nodes'}
					</div>
				</a>
			{/each}
		</div>
	{/if}
</div>
