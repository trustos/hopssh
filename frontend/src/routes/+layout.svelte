<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { getAuth } from '$lib/stores/auth.svelte';
	import { getTheme } from '$lib/stores/theme.svelte';
	import { Toaster } from 'svelte-sonner';
	import '../app.css';

	const auth = getAuth();
	const theme = getTheme();

	let { children } = $props();

	const PUBLIC_ROUTES = ['/login', '/register', '/invite'];

	const isPublicRoute = $derived(
		PUBLIC_ROUTES.some((r) => page.url.pathname.startsWith(r))
	);

	const shouldRenderChildren = $derived(
		auth.loading || auth.isAuthenticated || isPublicRoute
	);

	onMount(() => {
		theme.init();
		auth.init();

		// Register Service Worker for proxy URL rewriting.
		// The SW intercepts requests from proxy tabs and rewrites absolute paths
		// to include the proxy prefix so proxied web apps load correctly.
		if ('serviceWorker' in navigator) {
			navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch(() => {});
		}
	});

	// Auth guard: redirect unauthenticated users to login, authenticated users away from login.
	$effect(() => {
		if (auth.loading) return;

		if (!auth.isAuthenticated && !isPublicRoute) {
			// Preserve the original URL (path + query) across the login round-trip.
			// The desktop app's "Continue with browser" flow lands at
			// /device?code=HOP-XXXX; without this, signing in dumps the user
			// at / and the device code is lost. The login page already reads
			// the ?redirect= param and uses it post-auth.
			//
			// Sanitize: only forward same-origin paths beginning with "/" to
			// prevent open-redirect to external URLs (e.g. attacker links
			// like /login?redirect=https://evil.example).
			const target = page.url.pathname + page.url.search;
			const safe = target.startsWith('/') && !target.startsWith('//') ? target : '/';
			goto(`/login?redirect=${encodeURIComponent(safe)}`).catch(() => {});
		}
		if (auth.isAuthenticated && (page.url.pathname === '/login' || page.url.pathname === '/register')) {
			const redirect = page.url.searchParams.get('redirect');
			// Same sanitization on the post-auth side: a hostile
			// /login?redirect=https://evil.example must not redirect off-site.
			const safe = redirect && redirect.startsWith('/') && !redirect.startsWith('//') ? redirect : '/';
			goto(safe).catch(() => {});
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

<Toaster position="bottom-right" richColors />
