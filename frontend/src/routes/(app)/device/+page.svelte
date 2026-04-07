<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { device as deviceApi, networks as networksApi } from '$lib/api/client';
	import { ApiError } from '$lib/api/client';
	import type { NetworkResponse } from '$lib/types/api';

	let code = $state(page.url.searchParams.get('code') || '');
	let selectedNetwork = $state('');
	let networkList = $state<NetworkResponse[]>([]);
	let error = $state('');
	let loadError = $state('');
	let success = $state(false);
	let submitting = $state(false);
	let loadingNetworks = $state(true);

	onMount(async () => {
		try {
			networkList = await networksApi.list();
			const adminNetworks = networkList.filter(n => n.role === 'admin');
			if (adminNetworks.length > 0) {
				selectedNetwork = adminNetworks[0].id;
			}
		} catch (e) {
			loadError = e instanceof Error ? e.message : 'Failed to load networks';
		} finally {
			loadingNetworks = false;
		}
	});

	async function handleSubmit(e: Event) {
		e.preventDefault();
		error = '';
		submitting = true;
		try {
			await deviceApi.authorize(code, selectedNetwork);
			success = true;
		} catch (e) {
			error = e instanceof ApiError ? e.message : 'Authorization failed';
		} finally {
			submitting = false;
		}
	}
</script>

<svelte:head>
	<title>Device Auth - hopssh</title>
</svelte:head>

<div class="flex items-center justify-center p-6">
	<div class="w-full max-w-sm space-y-6">
		<div class="text-center">
			<h1 class="text-2xl font-bold">Authorize Device</h1>
			<p class="mt-1 text-sm text-muted-foreground">Enter the code shown on your server</p>
		</div>

		{#if success}
			<div class="rounded-lg border border-primary/50 bg-primary/10 p-6 text-center">
				<div class="mb-2 text-4xl">✓</div>
				<p class="font-medium text-primary">Device authorized!</p>
				<p class="mt-1 text-sm text-muted-foreground">The agent will connect momentarily.</p>
			</div>
		{:else if loadError}
			<div class="rounded-md bg-destructive/10 p-4 text-sm text-destructive">{loadError}</div>
		{:else if loadingNetworks}
			<div class="flex items-center justify-center py-8">
				<div class="h-6 w-6 animate-spin rounded-full border-2 border-primary border-t-transparent"></div>
			</div>
		{:else if networkList.filter(n => n.role === 'admin').length === 0}
			<div class="rounded-lg border border-dashed p-6 text-center">
				{#if networkList.length === 0}
					<p class="mb-1 text-sm font-medium">No networks yet</p>
					<p class="text-sm text-muted-foreground">Create a network first to authorize devices.</p>
					<a href="/" class="mt-2 inline-block text-sm text-primary hover:underline">Go to Networks</a>
				{:else}
					<p class="mb-1 text-sm font-medium">No admin access</p>
					<p class="text-sm text-muted-foreground">You need admin access to a network to authorize devices. Ask your network admin to authorize the device for you.</p>
				{/if}
			</div>
		{:else}
			<form onsubmit={handleSubmit} class="space-y-4">
				{#if error}
					<div class="rounded-md bg-destructive/10 p-3 text-sm text-destructive">{error}</div>
				{/if}

				<div class="space-y-2">
					<label for="code" class="text-sm font-medium">Device Code</label>
					<input
						id="code"
						type="text"
						bind:value={code}
						required
						placeholder="HOP-K9M2"
						class="w-full rounded-md border bg-background px-3 py-2 text-center font-mono text-lg uppercase tracking-widest focus:outline-none focus:ring-2 focus:ring-ring"
					/>
				</div>

				<div class="space-y-2">
					<label for="network" class="text-sm font-medium">Network</label>
					<select
						id="network"
						bind:value={selectedNetwork}
						required
						class="w-full rounded-md border bg-background px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
					>
						{#each networkList.filter(n => n.role === 'admin') as network}
							<option value={network.id}>{network.name}</option>
						{/each}
					</select>
				</div>

				<button
					type="submit"
					disabled={submitting || !code || !selectedNetwork}
					class="w-full rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
				>
					{submitting ? 'Authorizing...' : 'Authorize'}
				</button>
			</form>
		{/if}
	</div>
</div>
