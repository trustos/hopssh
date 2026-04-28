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

	// stripCode normalizes any clipboard form to the 4-char base32 body.
	// The desktop app's "Copy with HOP-" button copies "HOP-HKST"; the
	// "Copy 4 chars" button copies just "HKST". Either pasted into the
	// OTP needs to land as HKST. Single source of truth used at:
	//   - URL ?code= param parse
	//   - bits-ui PinInput pasteTransformer
	//   - oninput sanity check
	function stripCode(raw: string): string {
		return raw.replace(/^\s*HOP-?/i, '').replace(/[^A-Z0-9]/gi, '').toUpperCase();
	}

	const rawCode = page.url.searchParams.get('code') || '';
	let code = $state(stripCode(rawCode));
	let selectedNetwork = $state('');
	let networkList = $state<NetworkResponse[]>([]);
	let error = $state('');
	let loadError = $state('');
	let success = $state(false);
	let submitting = $state(false);
	let loadingNetworks = $state(true);

	// arrivedWithCode: the user came from the desktop app (URL had ?code=).
	// Combined with single-admin-network and signed-in, that's the
	// "no human decision needed" state where we auto-submit. See
	// onMount + the trio of conditions there.
	const arrivedWithCode = !!rawCode;

	const adminNetworks = $derived(networkList.filter(n => n.role === 'admin'));
	const fullCode = $derived('HOP-' + code.toUpperCase());

	// Stage drives the 3-step progress ladder. Mirrors the desktop's
	// onboarding ladder so the user sees a consistent narrative
	// across devices: code received → confirming → authorized.
	let stage = $derived<'code' | 'confirming' | 'authorized'>(
		success ? 'authorized' : submitting ? 'confirming' : 'code'
	);
	const steps = $derived([
		{ label: 'Code received', done: code.length === 4, active: code.length < 4 },
		{ label: 'Authorize',     done: success,            active: !success && code.length === 4 },
		{ label: 'Done',          done: success,            active: false }
	]);

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
			// Auto-submit when the trio of conditions holds:
			//   - user arrived with ?code= from the desktop opener
			//   - exactly one admin network (no human decision)
			//   - code parses to 4 chars after the strip
			// Anything else (multiple networks, paste-from-clipboard,
			// signed-out round-trip) requires a click.
			if (
				arrivedWithCode &&
				code.length === 4 &&
				adminNetworks.length === 1 &&
				!error &&
				!success
			) {
				selectedNetwork = adminNetworks[0].id;
				handleSubmit(new Event('submit'));
			}
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
	<title>Authorize Device - hopssh</title>
</svelte:head>

<div class="flex items-center justify-center p-6">
	<div class="w-full max-w-sm space-y-6">
		<div class="text-center">
			<h1 class="text-2xl font-bold">Authorize Device</h1>
			<p class="mt-1 text-sm text-muted-foreground">
				{#if arrivedWithCode && code.length === 4}
					Approve the Mac that asked to join.
				{:else}
					Enter the code shown on the device that's joining.
				{/if}
			</p>
		</div>

		<!-- Progress ladder mirrors the desktop's "Open browser → Approve →
		     Bring mesh up". Always visible so the user knows what step they
		     are at and what comes next. -->
		<ol class="flex items-center justify-between gap-2">
			{#each steps as s, i}
				<li class="flex flex-1 items-center gap-2">
					<span
						class={
							s.done
								? 'inline-flex h-6 w-6 items-center justify-center rounded-full bg-primary text-primary-foreground text-[11px] font-semibold'
								: s.active
									? 'inline-flex h-6 w-6 items-center justify-center rounded-full border-2 border-primary text-primary text-[11px] font-semibold'
									: 'inline-flex h-6 w-6 items-center justify-center rounded-full border border-muted-foreground/30 text-muted-foreground text-[11px]'
						}
					>
						{#if s.done}✓{:else}{i + 1}{/if}
					</span>
					<span class={s.done || s.active ? 'text-sm' : 'text-sm text-muted-foreground'}>
						{s.label}
					</span>
				</li>
			{/each}
		</ol>

		{#if success}
			<Card.Root class="border-primary/50 bg-primary/10">
				<Card.Content class="py-6 text-center">
					<CheckCircle class="mx-auto mb-2 size-8 text-primary" />
					<p class="font-medium text-primary">Device authorized</p>
					<p class="mt-1 text-sm text-muted-foreground">
						Return to hopssh on your device — the mesh will come up automatically.
					</p>
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
									pasteTransformer={stripCode}
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

						<!-- When there's only one admin network we lock the picker
						     to that single option. Multi-network admins still get
						     the full Select with their full list. -->
						{#if adminNetworks.length === 1}
							<div class="space-y-2">
								<Label>Network</Label>
								<div class="rounded-md border bg-muted/30 px-3 py-2 text-sm">
									{adminNetworks[0].name}
								</div>
							</div>
						{:else}
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
						{/if}

						<Button
							type="submit"
							class="w-full"
							disabled={submitting || code.length < 4 || !selectedNetwork}
						>
							{submitting ? 'Authorizing…' : 'Authorize'}
						</Button>
					</form>
				</Card.Content>
			</Card.Root>
		{/if}
	</div>
</div>
