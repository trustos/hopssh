<script lang="ts">
	import { onMount } from 'svelte';
	import { networks as networksApi } from '$lib/api/client';
	import { ApiError } from '$lib/api/client';
	import type { NetworkResponse } from '$lib/types/api';

	let networkList = $state<NetworkResponse[]>([]);
	let loading = $state(true);
	let error = $state('');

	// Create network dialog
	let showCreate = $state(false);
	let newName = $state('');
	let newDomain = $state('hop');
	let creating = $state(false);
	let createError = $state('');

	onMount(async () => {
		await loadNetworks();
	});

	async function loadNetworks() {
		loading = true;
		error = '';
		try {
			networkList = await networksApi.list();
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to load networks';
		} finally {
			loading = false;
		}
	}

	async function createNetwork(e: Event) {
		e.preventDefault();
		if (!newName.trim()) return;
		creating = true;
		createError = '';
		try {
			await networksApi.create(newName.trim(), newDomain.trim() || undefined);
			showCreate = false;
			newName = '';
			newDomain = 'hop';
			await loadNetworks();
		} catch (e) {
			createError = e instanceof ApiError ? e.message : 'Failed to create network';
		} finally {
			creating = false;
		}
	}
</script>

<svelte:head>
	<title>Networks - hopssh</title>
</svelte:head>

<div class="p-6">
	<div class="mb-6 flex items-center justify-between">
		<h1 class="text-2xl font-semibold">Networks</h1>
		<button
			onclick={() => (showCreate = true)}
			class="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90"
		>
			Create Network
		</button>
	</div>

	<!-- Create Network Dialog -->
	{#if showCreate}
		<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
			<div class="w-full max-w-sm rounded-lg border bg-card p-6 shadow-lg">
				<h2 class="mb-4 text-lg font-semibold">Create Network</h2>
				<form onsubmit={createNetwork} class="space-y-4">
					{#if createError}
						<div class="rounded-md bg-destructive/10 p-3 text-sm text-destructive">{createError}</div>
					{/if}
					<div class="space-y-2">
						<label for="network-name" class="text-sm font-medium">Name</label>
						<input
							id="network-name"
							type="text"
							bind:value={newName}
							required
							placeholder="production"
							class="w-full rounded-md border bg-background px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
						/>
					</div>
					<div class="space-y-2">
						<label for="dns-domain" class="text-sm font-medium">DNS Domain</label>
						<div class="flex items-center gap-2">
							<span class="text-sm text-muted-foreground">hostname.</span>
							<input
								id="dns-domain"
								type="text"
								bind:value={newDomain}
								placeholder="hop"
								class="w-24 rounded-md border bg-background px-3 py-2 text-sm font-mono focus:outline-none focus:ring-2 focus:ring-ring"
							/>
						</div>
						<p class="text-xs text-muted-foreground">
							Nodes will be reachable as <span class="font-mono">hostname.{newDomain || 'hop'}</span>
						</p>
					</div>
					<div class="flex justify-end gap-2">
						<button
							type="button"
							onclick={() => { showCreate = false; createError = ''; }}
							class="rounded-md px-4 py-2 text-sm hover:bg-accent"
						>
							Cancel
						</button>
						<button
							type="submit"
							disabled={creating || !newName.trim()}
							class="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
						>
							{creating ? 'Creating...' : 'Create'}
						</button>
					</div>
				</form>
			</div>
		</div>
	{/if}

	{#if loading}
		<div class="space-y-3">
			{#each Array(3) as _}
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
			<p class="mb-4 text-sm text-muted-foreground">Create your first network to start adding servers.</p>
			<button
				onclick={() => (showCreate = true)}
				class="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90"
			>
				Create Network
			</button>
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
						<div class="flex gap-3 text-sm text-muted-foreground">
							<span class="font-mono">{network.subnet}</span>
							<span class="font-mono">.{network.dnsDomain}</span>
						</div>
					</div>
					<div class="text-sm text-muted-foreground">
						{network.nodeCount} {network.nodeCount === 1 ? 'node' : 'nodes'}
					</div>
				</a>
			{/each}
		</div>
	{/if}
</div>
