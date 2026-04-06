<script lang="ts">
	import { onMount } from 'svelte';
	import { device as deviceApi, networks as networksApi } from '$lib/api/client';
	import { ApiError } from '$lib/api/client';
	import type { NetworkResponse } from '$lib/types/api';

	let code = $state('');
	let selectedNetwork = $state('');
	let networkList = $state<NetworkResponse[]>([]);
	let error = $state('');
	let success = $state(false);
	let submitting = $state(false);

	onMount(async () => {
		try {
			networkList = await networksApi.list();
			if (networkList.length > 0) {
				selectedNetwork = networkList[0].id;
			}
		} catch {
			// User might not be authenticated — that's OK, form still shows
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

<div class="flex min-h-screen items-center justify-center bg-background">
	<div class="w-full max-w-sm space-y-6 p-6">
		<div class="text-center">
			<h1 class="text-2xl font-bold"><span class="text-primary">hop</span>ssh</h1>
			<p class="mt-1 text-sm text-muted-foreground">Authorize a device</p>
		</div>

		{#if success}
			<div class="rounded-lg border border-primary/50 bg-primary/10 p-6 text-center">
				<div class="mb-2 text-4xl">✓</div>
				<p class="font-medium text-primary">Device authorized!</p>
				<p class="mt-1 text-sm text-muted-foreground">
					The agent will connect momentarily.
				</p>
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
						class="w-full rounded-md border bg-background px-3 py-2 text-center text-lg font-mono tracking-widest focus:outline-none focus:ring-2 focus:ring-ring uppercase"
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
						{#each networkList as network}
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
