<script lang="ts">
	import { page } from '$app/state';
	import { goto } from '$app/navigation';
	import { onMount } from 'svelte';
	import { invites, ApiError } from '$lib/api/client';
	import { getAuth } from '$lib/stores/auth.svelte';
	import type { InviteDetailResponse } from '$lib/types/api';
	import * as Card from '$lib/components/ui/card/index.js';
	import * as Alert from '$lib/components/ui/alert/index.js';
	import { Button } from '$lib/components/ui/button/index.js';
	import { Badge } from '$lib/components/ui/badge/index.js';
	import { Skeleton } from '$lib/components/ui/skeleton/index.js';
	import { CheckCircle } from 'lucide-svelte';

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
					goto(`/login?redirect=${encodeURIComponent(`/invite/${code}?auto=1`)}`);
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
			<h1 class="text-2xl font-bold"><span class="text-primary">hop</span>ssh</h1>
		</div>

		{#if loading}
			<Card.Root>
				<Card.Content class="space-y-3 py-6">
					<Skeleton class="mx-auto h-6 w-48" />
					<Skeleton class="mx-auto h-4 w-64" />
					<Skeleton class="h-10 w-full" />
				</Card.Content>
			</Card.Root>
		{:else if error}
			<Card.Root>
				<Card.Content class="py-6 text-center">
					<Alert.Root variant="destructive" class="mb-4">
						<Alert.Description>{error}</Alert.Description>
					</Alert.Root>
					<a href="/login" class="text-sm text-primary hover:underline">Go to login</a>
				</Card.Content>
			</Card.Root>
		{:else if accepted}
			<Card.Root class="border-primary/50 bg-primary/10">
				<Card.Content class="py-6 text-center">
					<CheckCircle class="mx-auto mb-2 size-8 text-primary" />
					<p class="mb-2 font-medium">Joined {invite?.networkName}!</p>
					<p class="text-sm text-muted-foreground">Redirecting to network...</p>
				</Card.Content>
			</Card.Root>
		{:else if invite}
			<Card.Root>
				<Card.Header>
					<Card.Title>Join "{invite.networkName}"</Card.Title>
					<Card.Description>
						You've been invited to join this network as
						<Badge variant="secondary" class="ml-1">{invite.role}</Badge>
					</Card.Description>
				</Card.Header>
				<Card.Content class="space-y-4">
					{#if invite.expiresAt}
						{@const remaining = invite.expiresAt - Math.floor(Date.now() / 1000)}
						{#if remaining > 0}
							<p class="text-xs text-muted-foreground">
								This invite expires in {remaining > 86400 ? `${Math.floor(remaining / 86400)} days` : remaining > 3600 ? `${Math.floor(remaining / 3600)} hours` : `${Math.floor(remaining / 60)} minutes`}.
							</p>
						{/if}
					{/if}

					{#if authStore.user}
						<Button class="w-full" onclick={acceptInvite} disabled={accepting}>
							{accepting ? 'Joining...' : 'Join Network'}
						</Button>
					{:else}
						<div class="space-y-2">
							<Button class="w-full" href="/login?redirect={encodeURIComponent(`/invite/${code}?auto=1`)}">
								Log in to join
							</Button>
							<Button variant="outline" class="w-full" href="/register?redirect={encodeURIComponent(`/invite/${code}?auto=1`)}">
								Register to join
							</Button>
						</div>
					{/if}
				</Card.Content>
			</Card.Root>
		{/if}
	</div>
</div>
