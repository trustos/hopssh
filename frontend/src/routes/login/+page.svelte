<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { getAuth } from '$lib/stores/auth.svelte';
	import { auth as authApi, ApiError } from '$lib/api/client';
	import * as Card from '$lib/components/ui/card/index.js';
	import * as Alert from '$lib/components/ui/alert/index.js';
	import { Button } from '$lib/components/ui/button/index.js';
	import { Input } from '$lib/components/ui/input/index.js';
	import { Label } from '$lib/components/ui/label/index.js';
	import { Separator } from '$lib/components/ui/separator/index.js';

	const auth = getAuth();
	const redirectTo = $derived(page.url.searchParams.get('redirect') || '/');

	let email = $state('');
	let password = $state('');
	let error = $state('');
	let submitting = $state(false);
	let noUsers = $state(false);

	onMount(async () => {
		try {
			const status = await authApi.status();
			noUsers = !status.hasUsers;
		} catch {
			// Can't reach server — show login form anyway
		}
	});

	async function handleSubmit(e: Event) {
		e.preventDefault();
		error = '';
		submitting = true;
		try {
			await auth.login(email, password);
			goto(redirectTo);
		} catch (e) {
			if (e instanceof ApiError) {
				if (e.status === 401) {
					error = 'Invalid email or password';
				} else {
					error = e.message;
				}
			} else {
				error = 'Could not reach the server. Is it running?';
			}
		} finally {
			submitting = false;
		}
	}
</script>

<svelte:head>
	<title>Sign in - hopssh</title>
</svelte:head>

<div class="flex min-h-screen items-center justify-center bg-background p-4">
	<div class="w-full max-w-sm space-y-6">
		<div class="text-center">
			<h1 class="text-2xl font-bold"><span class="text-primary">hop</span>ssh</h1>
			<p class="mt-1 text-sm text-muted-foreground">Hop into your network</p>
		</div>

		{#if noUsers}
			<Alert.Root class="border-primary/50 bg-primary/10">
				<Alert.Title>Welcome! No accounts exist yet.</Alert.Title>
				<Alert.Description>
					<a href="/register" class="font-medium text-primary hover:underline">
						Create your first account &rarr;
					</a>
				</Alert.Description>
			</Alert.Root>
		{/if}

		<Card.Root>
			<Card.Content class="space-y-4">
				<!-- OAuth providers (ready for roadmap #3 GitHub OAuth) -->
				<div class="space-y-2">
					<Button variant="outline" class="w-full" disabled>
						<svg class="size-4" viewBox="0 0 24 24" fill="currentColor"><path d="M12 0C5.37 0 0 5.37 0 12c0 5.31 3.435 9.795 8.205 11.385.6.105.825-.255.825-.57 0-.285-.015-1.23-.015-2.235-3.015.555-3.795-.735-4.035-1.41-.135-.345-.72-1.41-1.23-1.695-.42-.225-1.02-.78-.015-.795.945-.015 1.62.87 1.845 1.23 1.08 1.815 2.805 1.305 3.495.99.105-.78.42-1.305.765-1.605-2.67-.3-5.46-1.335-5.46-5.925 0-1.305.465-2.385 1.23-3.225-.12-.3-.54-1.53.12-3.18 0 0 1.005-.315 3.3 1.23.96-.27 1.98-.405 3-.405s2.04.135 3 .405c2.295-1.56 3.3-1.23 3.3-1.23.66 1.65.24 2.88.12 3.18.765.84 1.23 1.905 1.23 3.225 0 4.605-2.805 5.625-5.475 5.925.435.375.81 1.095.81 2.22 0 1.605-.015 2.895-.015 3.3 0 .315.225.69.825.57A12.02 12.02 0 0 0 24 12c0-6.63-5.37-12-12-12z"/></svg>
						Continue with GitHub
					</Button>
				</div>

				<div class="relative">
					<div class="absolute inset-0 flex items-center">
						<Separator />
					</div>
					<div class="relative flex justify-center text-xs uppercase">
						<span class="bg-card px-2 text-muted-foreground">or</span>
					</div>
				</div>

				<!-- Email/password form -->
				<form onsubmit={handleSubmit} class="space-y-4">
					{#if error}
						<Alert.Root variant="destructive">
							<Alert.Description>{error}</Alert.Description>
						</Alert.Root>
					{/if}

					<div class="space-y-2">
						<Label for="email">Email</Label>
						<Input
							id="email"
							type="email"
							bind:value={email}
							required
							placeholder="you@example.com"
						/>
					</div>

					<div class="space-y-2">
						<Label for="password">Password</Label>
						<Input
							id="password"
							type="password"
							bind:value={password}
							required
							minlength={8}
						/>
					</div>

					<Button type="submit" class="w-full" disabled={submitting}>
						{submitting ? 'Signing in...' : 'Sign in'}
					</Button>
				</form>
			</Card.Content>
		</Card.Root>

		<p class="text-center text-sm text-muted-foreground">
			No account? <a href="/register" class="text-primary hover:underline">Create one</a>
		</p>
	</div>
</div>
