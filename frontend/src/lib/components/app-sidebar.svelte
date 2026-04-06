<script lang="ts">
	import { page } from '$app/state';
	import { getAuth } from '$lib/stores/auth.svelte';
	import { getTheme } from '$lib/stores/theme.svelte';

	const auth = getAuth();
	const theme = getTheme();
	let { children } = $props();
</script>

<div class="flex h-screen">
	<!-- Sidebar -->
	<aside class="flex w-56 flex-col border-r bg-card">
		<div class="p-4 text-lg font-bold tracking-tight">
			<span class="text-primary">hop</span>ssh
		</div>

		<nav class="flex-1 space-y-1 px-2">
			<a
				href="/"
				class="flex items-center gap-2 rounded-md px-3 py-2 text-sm hover:bg-accent"
				class:bg-accent={page.url.pathname === '/'}
			>
				Networks
			</a>
			<a
				href="/device"
				class="flex items-center gap-2 rounded-md px-3 py-2 text-sm hover:bg-accent"
				class:bg-accent={page.url.pathname === '/device'}
			>
				Device Auth
			</a>
		</nav>

		<div class="space-y-2 border-t p-4">
			<div class="truncate text-sm text-muted-foreground">{auth.user?.email}</div>
			<div class="flex gap-2">
				<button
					onclick={theme.toggle}
					class="rounded-md p-2 text-sm hover:bg-accent"
				>
					{theme.mode === 'dark' ? '☀️' : '🌙'}
				</button>
				<button
					onclick={auth.logout}
					class="rounded-md p-2 text-sm text-muted-foreground hover:bg-accent hover:text-foreground"
				>
					Logout
				</button>
			</div>
		</div>
	</aside>

	<!-- Main content -->
	<main class="flex-1 overflow-auto">
		{@render children()}
	</main>
</div>
