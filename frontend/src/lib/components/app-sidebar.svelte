<script lang="ts">
	import { page } from '$app/state';
	import { getAuth } from '$lib/stores/auth.svelte';
	import { getTheme } from '$lib/stores/theme.svelte';

	const auth = getAuth();
	const theme = getTheme();
	let { children } = $props();

	const isNetworks = $derived(
		page.url.pathname === '/' || page.url.pathname.startsWith('/networks')
	);
	const isAudit = $derived(page.url.pathname.startsWith('/audit'));
	const isDevice = $derived(page.url.pathname.startsWith('/device'));
</script>

<div class="flex h-screen">
	<!-- Sidebar -->
	<aside class="flex w-56 flex-col border-r bg-card">
		<!-- Logo -->
		<div class="p-4 text-lg font-bold tracking-tight">
			<span class="text-primary">hop</span>ssh
		</div>

		<!-- Main nav -->
		<nav class="flex-1 space-y-1 px-2">
			<a
				href="/"
				class="flex items-center gap-2 rounded-md px-3 py-2 text-sm hover:bg-accent"
				class:bg-accent={isNetworks}
			>
				<svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><line x1="2" y1="12" x2="22" y2="12"/><path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z"/></svg>
				Networks
			</a>
			<a
				href="/device"
				class="flex items-center gap-2 rounded-md px-3 py-2 text-sm hover:bg-accent"
				class:bg-accent={isDevice}
			>
				<svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="5" y="2" width="14" height="20" rx="2" ry="2"/><line x1="12" y1="18" x2="12.01" y2="18"/></svg>
				Device Auth
			</a>
			<a
				href="/audit"
				class="flex items-center gap-2 rounded-md px-3 py-2 text-sm hover:bg-accent"
				class:bg-accent={isAudit}
			>
				<svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8z"/><polyline points="14 2 14 8 20 8"/><line x1="16" y1="13" x2="8" y2="13"/><line x1="16" y1="17" x2="8" y2="17"/><polyline points="10 9 9 9 8 9"/></svg>
				Audit Log
			</a>
		</nav>

		<!-- User section -->
		<div class="space-y-2 border-t p-4">
			<div class="truncate text-sm text-muted-foreground">{auth.user?.email}</div>
			<div class="flex gap-2">
				<button
					onclick={() => theme.toggle()}
					class="rounded-md p-2 text-sm hover:bg-accent"
					aria-label="Toggle dark mode"
				>
					{#if theme.mode === 'dark'}
						<svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><circle cx="12" cy="12" r="5"/><line x1="12" y1="1" x2="12" y2="3"/><line x1="12" y1="21" x2="12" y2="23"/><line x1="4.22" y1="4.22" x2="5.64" y2="5.64"/><line x1="18.36" y1="18.36" x2="19.78" y2="19.78"/><line x1="1" y1="12" x2="3" y2="12"/><line x1="21" y1="12" x2="23" y2="12"/><line x1="4.22" y1="19.78" x2="5.64" y2="18.36"/><line x1="18.36" y1="5.64" x2="19.78" y2="4.22"/></svg>
					{:else}
						<svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>
					{/if}
				</button>
				<button
					onclick={() => auth.logout()}
					class="rounded-md p-2 text-sm text-muted-foreground hover:bg-accent hover:text-foreground"
					aria-label="Sign out"
				>
					<svg class="h-4 w-4" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2"><path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4"/><polyline points="16 17 21 12 16 7"/><line x1="21" y1="12" x2="9" y2="12"/></svg>
				</button>
			</div>
		</div>
	</aside>

	<!-- Main content -->
	<main class="flex-1 overflow-auto">
		{@render children()}
	</main>
</div>
