<script lang="ts">
	import { onMount } from 'svelte';
	import { networks as networksApi } from '$lib/api/client';
	import { ApiError } from '$lib/api/client';
	import type { NetworkResponse } from '$lib/types/api';
	import * as Card from '$lib/components/ui/card/index.js';
	import * as Dialog from '$lib/components/ui/dialog/index.js';
	import * as Alert from '$lib/components/ui/alert/index.js';
	import { Button } from '$lib/components/ui/button/index.js';
	import { Input } from '$lib/components/ui/input/index.js';
	import { Label } from '$lib/components/ui/label/index.js';
	import { Badge } from '$lib/components/ui/badge/index.js';
	import { Skeleton } from '$lib/components/ui/skeleton/index.js';
	import { Globe, Plus } from 'lucide-svelte';

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
		<Dialog.Root bind:open={showCreate}>
			<Dialog.Trigger>
				{#snippet child({ props })}
					<Button {...props}>
						<Plus class="size-4" />
						Create Network
					</Button>
				{/snippet}
			</Dialog.Trigger>
			<Dialog.Content class="sm:max-w-sm">
				<Dialog.Header>
					<Dialog.Title>Create Network</Dialog.Title>
					<Dialog.Description>
						Set up a new isolated mesh network with its own PKI and DNS domain.
					</Dialog.Description>
				</Dialog.Header>
				<form onsubmit={createNetwork} class="space-y-4">
					{#if createError}
						<Alert.Root variant="destructive">
							<Alert.Description>{createError}</Alert.Description>
						</Alert.Root>
					{/if}
					<div class="space-y-2">
						<Label for="network-name">Name</Label>
						<Input
							id="network-name"
							type="text"
							bind:value={newName}
							required
							placeholder="production"
						/>
					</div>
					<div class="space-y-2">
						<Label for="dns-domain">Domain suffix</Label>
						<div class="flex items-center gap-2">
							<span class="text-sm text-muted-foreground">hostname</span>
							<span class="text-sm text-muted-foreground">.</span>
							<Input
								id="dns-domain"
								type="text"
								bind:value={newDomain}
								placeholder="hop"
								class="w-24 font-mono"
							/>
						</div>
						<p class="text-xs text-muted-foreground">
							Nodes will be reachable as
							<span class="font-mono font-medium">&lt;name&gt;.{newDomain || 'hop'}</span>
							— e.g. <span class="font-mono">jellyfin.{newDomain || 'hop'}</span>.
							This cannot be changed later.
						</p>
					</div>
					<Dialog.Footer>
						<Button
							type="button"
							variant="outline"
							onclick={() => { showCreate = false; createError = ''; }}
						>
							Cancel
						</Button>
						<Button type="submit" disabled={creating || !newName.trim()}>
							{creating ? 'Creating...' : 'Create'}
						</Button>
					</Dialog.Footer>
				</form>
			</Dialog.Content>
		</Dialog.Root>
	</div>

	{#if loading}
		<div class="space-y-3">
			{#each Array(3) as _}
				<Skeleton class="h-16 w-full rounded-lg" />
			{/each}
		</div>
	{:else if error}
		<Alert.Root variant="destructive">
			<Alert.Description>{error}</Alert.Description>
		</Alert.Root>
	{:else if networkList.length === 0}
		<Card.Root class="border-dashed">
			<Card.Content class="py-8 text-center">
				<Globe class="mx-auto mb-3 size-10 text-muted-foreground" />
				<p class="mb-2 text-lg font-medium">No networks yet</p>
				<p class="mb-4 text-sm text-muted-foreground">Create your first network to start adding servers.</p>
				<Button onclick={() => (showCreate = true)}>
					<Plus class="size-4" />
					Create Network
				</Button>
			</Card.Content>
		</Card.Root>
	{:else}
		<div class="space-y-3">
			{#each networkList as network}
				<a
					href="/networks/{network.id}"
					class="flex items-center justify-between rounded-lg border p-4 transition-colors hover:bg-accent"
				>
					<div>
						<div class="flex items-center gap-2">
							<span class="font-medium">{network.name}</span>
							{#if network.role === 'member'}
								<Badge variant="secondary">Shared</Badge>
							{/if}
						</div>
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
