<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { getAuth } from '$lib/stores/auth.svelte';
	import { getTheme } from '$lib/stores/theme.svelte';
	import '../app.css';

	const auth = getAuth();
	const theme = getTheme();

	let { children } = $props();

	const PUBLIC_ROUTES = ['/login', '/register', '/device'];

	onMount(() => {
		theme.init();
		auth.init();
	});

	$effect(() => {
		if (auth.loading) return;
		const path = page.url.pathname;
		const isPublic = PUBLIC_ROUTES.some((r) => path.startsWith(r));

		if (!auth.isAuthenticated && !isPublic) {
			goto('/login');
		}
		if (auth.isAuthenticated && (path === '/login' || path === '/register')) {
			goto('/');
		}
	});
</script>

{#if auth.loading}
	<div class="flex h-screen items-center justify-center bg-background">
		<div
			class="h-8 w-8 animate-spin rounded-full border-4 border-primary border-t-transparent"
		></div>
	</div>
{:else}
	{@render children()}
{/if}
