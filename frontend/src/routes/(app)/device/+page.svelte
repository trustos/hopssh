<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { device as deviceApi, networks as networksApi } from '$lib/api/client';
	import { ApiError } from '$lib/api/client';
	import type { NetworkResponse } from '$lib/types/api';
	import * as Card from '$lib/components/ui/card/index.js';
	import * as Alert from '$lib/components/ui/alert/index.js';
	import * as Select from '$lib/components/ui/select/index.js';
	import * as InputOTP from '$lib/components/ui/input-otp/index.js';
	import { Button } from '$lib/components/ui/button/index.js';
	import { Label } from '$lib/components/ui/label/index.js';
	import { Skeleton } from '$lib/components/ui/skeleton/index.js';
	import { CheckCircle, Minus } from 'lucide-svelte';

	// Strip "HOP-" prefix if pasted or from URL param
	const rawCode = page.url.searchParams.get('code') || '';
	let code = $state(rawCode.replace(/^HOP-/i, '').toUpperCase());
	let selectedNetwork = $state('');
	let networkList = $state<NetworkResponse[]>([]);
	let error = $state('');
	let loadError = $state('');
	let success = $state(false);
	let submitting = $state(false);
	let loadingNetworks = $state(true);

	const adminNetworks = $derived(networkList.filter(n => n.role === 'admin'));
	const fullCode = $derived('HOP-' + code.toUpperCase());

	onMount(async () => {
		try {
			networkList = await networksApi.list();
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
			await deviceApi.authorize(fullCode, selectedNetwork);
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
			<Card.Root class="border-primary/50 bg-primary/10">
				<Card.Content class="py-6 text-center">
					<CheckCircle class="mx-auto mb-2 size-8 text-primary" />
					<p class="font-medium text-primary">Device authorized!</p>
					<p class="mt-1 text-sm text-muted-foreground">The agent will connect momentarily.</p>
				</Card.Content>
			</Card.Root>
		{:else if loadError}
			<Alert.Root variant="destructive">
				<Alert.Description>{loadError}</Alert.Description>
			</Alert.Root>
		{:else if loadingNetworks}
			<Card.Root>
				<Card.Content class="space-y-4 py-6">
					<Skeleton class="mx-auto h-12 w-48" />
					<Skeleton class="h-10 w-full" />
					<Skeleton class="h-10 w-full" />
				</Card.Content>
			</Card.Root>
		{:else if adminNetworks.length === 0}
			<Card.Root class="border-dashed">
				<Card.Content class="py-6 text-center">
					{#if networkList.length === 0}
						<p class="mb-1 text-sm font-medium">No networks yet</p>
						<p class="text-sm text-muted-foreground">Create a network first to authorize devices.</p>
						<a href="/" class="mt-2 inline-block text-sm text-primary hover:underline">Go to Networks</a>
					{:else}
						<p class="mb-1 text-sm font-medium">No admin access</p>
						<p class="text-sm text-muted-foreground">You need admin access to a network to authorize devices.</p>
					{/if}
				</Card.Content>
			</Card.Root>
		{:else}
			<Card.Root>
				<Card.Content class="space-y-6">
					<form onsubmit={handleSubmit} class="space-y-6">
						{#if error}
							<Alert.Root variant="destructive">
								<Alert.Description>{error}</Alert.Description>
							</Alert.Root>
						{/if}

						<div class="space-y-3">
							<Label>Device Code</Label>
							<div class="flex items-center justify-center gap-2">
								<span class="font-mono text-lg font-semibold text-muted-foreground">HOP</span>
								<Minus class="size-4 text-muted-foreground" />
								<InputOTP.Root
									bind:value={code}
									maxlength={4}
									class="justify-center"
									onComplete={() => {
										if (selectedNetwork && code.length === 4) {
											handleSubmit(new Event('submit'));
										}
									}}
								>
									{#snippet children({ cells })}
										<InputOTP.Group>
											{#each cells as cell}
												<InputOTP.Slot {cell} />
											{/each}
										</InputOTP.Group>
									{/snippet}
								</InputOTP.Root>
							</div>
						</div>

						<div class="space-y-2">
							<Label>Network</Label>
							<Select.Root type="single" bind:value={selectedNetwork}>
								<Select.Trigger class="w-full">
									{@const selected = adminNetworks.find(n => n.id === selectedNetwork)}
									<span>{selected?.name || 'Select a network'}</span>
								</Select.Trigger>
								<Select.Content>
									{#each adminNetworks as network}
										<Select.Item value={network.id}>{network.name}</Select.Item>
									{/each}
								</Select.Content>
							</Select.Root>
						</div>

						<Button
							type="submit"
							class="w-full"
							disabled={submitting || code.length < 4 || !selectedNetwork}
						>
							{submitting ? 'Authorizing...' : 'Authorize'}
						</Button>
					</form>
				</Card.Content>
			</Card.Root>
		{/if}
	</div>
</div>
