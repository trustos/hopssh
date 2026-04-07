<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { getAuth } from '$lib/stores/auth.svelte';
	import { auth as authApi, ApiError } from '$lib/api/client';

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

<div class="flex min-h-screen items-center justify-center bg-background">
	<div class="w-full max-w-sm space-y-6 p-6">
		<div class="text-center">
			<h1 class="text-2xl font-bold"><span class="text-primary">hop</span>ssh</h1>
			<p class="mt-1 text-sm text-muted-foreground">Hop into your network</p>
		</div>

		{#if noUsers}
			<div class="rounded-lg border border-primary/50 bg-primary/10 p-4 text-center">
				<p class="font-medium">Welcome! No accounts exist yet.</p>
				<a href="/register" class="mt-2 inline-block text-sm font-medium text-primary hover:underline">
					Create your first account →
				</a>
			</div>
		{/if}

		<form onsubmit={handleSubmit} class="space-y-4">
			{#if error}
				<div class="rounded-md bg-destructive/10 p-3 text-sm text-destructive">{error}</div>
			{/if}

				<div class="space-y-2">
					<label for="email" class="text-sm font-medium">Email</label>
					<input
						id="email"
						type="email"
						bind:value={email}
						required
						class="w-full rounded-md border bg-background px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
						placeholder="you@example.com"
					/>
				</div>

				<div class="space-y-2">
					<label for="password" class="text-sm font-medium">Password</label>
					<input
						id="password"
						type="password"
						bind:value={password}
						required
						minlength={8}
						class="w-full rounded-md border bg-background px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
					/>
				</div>

				<button
					type="submit"
					disabled={submitting}
					class="w-full rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
				>
					{submitting ? 'Signing in...' : 'Sign in'}
				</button>
			</form>

			<p class="text-center text-sm text-muted-foreground">
				No account? <a href="/register" class="text-primary hover:underline">Create one</a>
			</p>
		</div>
	</div>
