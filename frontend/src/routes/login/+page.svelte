<script lang="ts">
	import { goto } from '$app/navigation';
	import { getAuth } from '$lib/stores/auth.svelte';
	import { ApiError } from '$lib/api/client';

	const auth = getAuth();

	let email = $state('');
	let password = $state('');
	let error = $state('');
	let submitting = $state(false);

	async function handleSubmit(e: Event) {
		e.preventDefault();
		error = '';
		submitting = true;
		try {
			await auth.login(email, password);
			goto('/');
		} catch (e) {
			error = e instanceof ApiError ? e.message : 'Login failed';
		} finally {
			submitting = false;
		}
	}
</script>

<div class="flex min-h-screen items-center justify-center bg-background">
	<div class="w-full max-w-sm space-y-6 p-6">
		<div class="text-center">
			<h1 class="text-2xl font-bold"><span class="text-primary">hop</span>ssh</h1>
			<p class="mt-1 text-sm text-muted-foreground">Hop into your servers</p>
		</div>

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
			No account? <a href="/register" class="text-primary hover:underline">Register</a>
		</p>
	</div>
</div>
