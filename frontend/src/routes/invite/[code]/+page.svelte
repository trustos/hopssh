<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';
	import { invites, ApiError } from '$lib/api/client';
	import { getAuth } from '$lib/stores/auth.svelte';
	import type { InviteDetailResponse } from '$lib/types/api';

	const authStore = getAuth();

	const code = $derived(page.params.code!);

	let invite = $state<InviteDetailResponse | null>(null);
	let loading = $state(true);
	let error = $state('');
	let accepting = $state(false);
	let accepted = $state(false);

	// Check if user returned from login redirect — auto-accept if so.
	const autoAccept = $derived(page.url.searchParams.get('auto') === '1');

	onMount(async () => {
		await loadInvite();
		// If user is logged in and came from a redirect, auto-accept.
		if (autoAccept && authStore.user && invite && !error) {
			acceptInvite();
		}
	});

	async function loadInvite() {
		try {
			invite = await invites.get(code);
		} catch (e) {
			if (e instanceof ApiError) {
				if (e.status === 404) error = 'This invite link is invalid.';
				else if (e.status === 410) error = 'This invite has expired or reached its usage limit.';
				else error = e.message;
			} else {
				error = 'Failed to load invite details.';
			}
		} finally {
			loading = false;
		}
	}

	async function acceptInvite() {
		accepting = true;
		try {
			const result = await invites.accept(code);
			accepted = true;
			setTimeout(() => goto(`/networks/${result.networkId}`), 1500);
		} catch (e) {
			if (e instanceof ApiError) {
				if (e.status === 401) {
					goto(`/login?redirect=/invite/${code}?auto=1`);
					return;
				}
				error = e.message;
			} else {
				error = 'Failed to accept invite.';
			}
		} finally {
			accepting = false;
		}
	}
</script>

<svelte:head>
	<title>{invite ? `Join ${invite.networkName}` : 'Accept Invite'} - hopssh</title>
</svelte:head>

<div class="flex min-h-screen items-center justify-center bg-background p-4">
	<div class="w-full max-w-sm space-y-6">
		<div class="text-center">
			<h1 class="text-2xl font-semibold">hopssh</h1>
		</div>

		{#if loading}
			<div class="rounded-lg border bg-card p-6">
				<div class="flex items-center justify-center gap-3 py-4">
					<div class="h-5 w-5 animate-spin rounded-full border-2 border-primary border-t-transparent"></div>
					<span class="text-sm text-muted-foreground">Loading invite...</span>
				</div>
			</div>
		{:else if error}
			<div class="rounded-lg border bg-card p-6 text-center">
				<p class="mb-4 text-sm text-destructive">{error}</p>
				<a href="/login" class="text-sm text-primary hover:underline">Go to login</a>
			</div>
		{:else if accepted}
			<div class="rounded-lg border bg-card p-6 text-center">
				<div class="mb-2 text-2xl">&#10003;</div>
				<p class="mb-2 font-medium">Joined {invite?.networkName}!</p>
				<p class="text-sm text-muted-foreground">Redirecting to network...</p>
			</div>
		{:else if invite}
			<div class="rounded-lg border bg-card p-6">
				<h2 class="mb-1 text-lg font-semibold">Join "{invite.networkName}"</h2>
				<p class="mb-4 text-sm text-muted-foreground">
					You've been invited to join this network as a <span class="font-medium">{invite.role}</span>.
				</p>

				{#if invite.expiresAt}
					{@const remaining = invite.expiresAt - Math.floor(Date.now() / 1000)}
					{#if remaining > 0}
						<p class="mb-4 text-xs text-muted-foreground">
							This invite expires in {remaining > 86400 ? `${Math.floor(remaining / 86400)} days` : remaining > 3600 ? `${Math.floor(remaining / 3600)} hours` : `${Math.floor(remaining / 60)} minutes`}.
						</p>
					{/if}
				{/if}

				{#if authStore.user}
					<button
						onclick={acceptInvite}
						disabled={accepting}
						class="w-full rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
					>
						{accepting ? 'Joining...' : 'Join Network'}
					</button>
				{:else}
					<div class="space-y-2">
						<a
							href="/login?redirect=/invite/{code}?auto=1"
							class="block w-full rounded-md bg-primary px-4 py-2 text-center text-sm font-medium text-primary-foreground hover:bg-primary/90"
						>
							Log in to join
						</a>
						<a
							href="/register?redirect=/invite/{code}?auto=1"
							class="block w-full rounded-md border px-4 py-2 text-center text-sm font-medium hover:bg-accent"
						>
							Register to join
						</a>
					</div>
				{/if}
			</div>
		{/if}
	</div>
</div>
