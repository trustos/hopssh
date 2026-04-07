<script lang="ts">
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { getAuth } from '$lib/stores/auth.svelte';
	import { ApiError } from '$lib/api/client';

	const auth = getAuth();
	const redirectTo = $derived(page.url.searchParams.get('redirect') || '/');

	let email = $state('');
	let name = $state('');
	let password = $state('');
	let error = $state('');
	let submitting = $state(false);

	async function handleSubmit(e: Event) {
		e.preventDefault();
		error = '';
		submitting = true;
		try {
			await auth.register(email, name, password);
			goto(redirectTo);
		} catch (e) {
			error = e instanceof ApiError ? e.message : 'Registration failed';
		} finally {
			submitting = false;
		}
	}
</script>

<svelte:head>
	<title>Register - hopssh</title>
</svelte:head>

<div class="flex min-h-screen items-center justify-center bg-background">
	<div class="w-full max-w-sm space-y-6 p-6">
		<div class="text-center">
			<h1 class="text-2xl font-bold"><span class="text-primary">hop</span>ssh</h1>
			<p class="mt-1 text-sm text-muted-foreground">Create your account</p>
		</div>

		<form onsubmit={handleSubmit} class="space-y-4">
			{#if error}
				<div class="rounded-md bg-destructive/10 p-3 text-sm text-destructive">{error}</div>
			{/if}

			<div class="space-y-2">
				<label for="name" class="text-sm font-medium">Name</label>
				<input
					id="name"
					type="text"
					bind:value={name}
					required
					class="w-full rounded-md border bg-background px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
					placeholder="Jane Doe"
				/>
			</div>

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
					maxlength={72}
					class="w-full rounded-md border bg-background px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring"
				/>
				<p class="text-xs text-muted-foreground">8-72 characters</p>
			</div>

			<button
				type="submit"
				disabled={submitting}
				class="w-full rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
			>
				{submitting ? 'Creating account...' : 'Create account'}
			</button>
		</form>

		<p class="text-center text-sm text-muted-foreground">
			Already have an account? <a href="/login" class="text-primary hover:underline">Sign in</a>
		</p>
	</div>
</div>
