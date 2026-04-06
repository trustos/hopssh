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

	const PUBLIC_ROUTES = ['/login', '/register'];

	const isPublicRoute = $derived(
		PUBLIC_ROUTES.some((r) => page.url.pathname.startsWith(r))
	);

	const shouldRenderChildren = $derived(
		auth.loading || auth.isAuthenticated || isPublicRoute
	);

	onMount(() => {
		theme.init();
		auth.init();
	});

	// Auth guard: redirect unauthenticated users to login, authenticated users away from login.
	$effect(() => {
		if (auth.loading) return;

		if (!auth.isAuthenticated && !isPublicRoute) {
			goto('/login').catch(() => {});
		}
		if (auth.isAuthenticated && (page.url.pathname === '/login' || page.url.pathname === '/register')) {
			goto('/').catch(() => {});
		}
	});
</script>

{#if auth.loading}
	<div class="flex h-screen items-center justify-center bg-background">
		<div
			class="h-8 w-8 animate-spin rounded-full border-4 border-primary border-t-transparent"
		></div>
	</div>
{:else if shouldRenderChildren}
	{@render children()}
{/if}
