<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { getAuth } from '$lib/stores/auth.svelte';
	import { getTheme } from '$lib/stores/theme.svelte';
	import { getServerInfo } from '$lib/stores/server-info.svelte';
	import TerminalPane from '$lib/components/terminal-pane.svelte';
	import * as Sidebar from '$lib/components/ui/sidebar/index.js';
	import { Globe, Smartphone, FileText, Sun, Moon, LogOut, Server } from 'lucide-svelte';

	const auth = getAuth();
	const theme = getTheme();
	const serverInfo = getServerInfo();
	let { children } = $props();

	onMount(() => {
		serverInfo.init();
	});

	const isNetworks = $derived(
		page.url.pathname === '/' || page.url.pathname.startsWith('/networks')
	);
	const isAudit = $derived(page.url.pathname.startsWith('/audit'));
	const isDevice = $derived(page.url.pathname.startsWith('/device'));
</script>

<Sidebar.Provider>
	<Sidebar.Root collapsible="icon">
		<Sidebar.Header>
			<Sidebar.Menu>
				<Sidebar.MenuItem>
					<Sidebar.MenuButton size="lg">
						{#snippet child({ props })}
							<a href="/" {...props}>
								<div class="bg-primary text-primary-foreground flex size-8 items-center justify-center rounded-lg text-sm font-bold">
									h
								</div>
								<div class="grid flex-1 text-left text-sm leading-tight">
									<span class="truncate font-semibold"><span class="text-primary">hop</span>ssh</span>
									<span class="truncate text-xs text-muted-foreground">Mesh Networking</span>
								</div>
							</a>
						{/snippet}
					</Sidebar.MenuButton>
				</Sidebar.MenuItem>
			</Sidebar.Menu>
		</Sidebar.Header>

		<Sidebar.Content>
			<Sidebar.Group>
				<Sidebar.GroupLabel>Navigation</Sidebar.GroupLabel>
				<Sidebar.Menu>
					<Sidebar.MenuItem>
						<Sidebar.MenuButton isActive={isNetworks} tooltipContent="Networks">
							{#snippet child({ props })}
								<a href="/" {...props}>
									<Globe class="size-4" />
									<span>Networks</span>
								</a>
							{/snippet}
						</Sidebar.MenuButton>
					</Sidebar.MenuItem>
					<Sidebar.MenuItem>
						<Sidebar.MenuButton isActive={isDevice} tooltipContent="Device Auth">
							{#snippet child({ props })}
								<a href="/device" {...props}>
									<Smartphone class="size-4" />
									<span>Device Auth</span>
								</a>
							{/snippet}
						</Sidebar.MenuButton>
					</Sidebar.MenuItem>
					<Sidebar.MenuItem>
						<Sidebar.MenuButton isActive={isAudit} tooltipContent="Audit Log">
							{#snippet child({ props })}
								<a href="/audit" {...props}>
									<FileText class="size-4" />
									<span>Audit Log</span>
								</a>
							{/snippet}
						</Sidebar.MenuButton>
					</Sidebar.MenuItem>
				</Sidebar.Menu>
			</Sidebar.Group>
		</Sidebar.Content>

		<Sidebar.Footer>
			<Sidebar.Menu>
				<Sidebar.MenuItem>
					<Sidebar.MenuButton
						class="text-muted-foreground pointer-events-none"
						tooltipContent={serverInfo.current ? `Control plane ${serverInfo.current}` : undefined}
					>
						<Server class="size-4" />
						<span class="truncate font-mono text-xs">{serverInfo.current ?? '—'}</span>
					</Sidebar.MenuButton>
				</Sidebar.MenuItem>
				<Sidebar.MenuItem>
					<Sidebar.MenuButton class="text-muted-foreground pointer-events-none">
						<span class="truncate text-xs">{auth.user?.email}</span>
					</Sidebar.MenuButton>
				</Sidebar.MenuItem>
				<Sidebar.MenuItem>
					<div class="flex gap-1 px-2 group-data-[collapsible=icon]:px-0 group-data-[collapsible=icon]:justify-center">
						<button
							onclick={() => theme.toggle()}
							class="rounded-md p-2 text-sm text-muted-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground"
							aria-label="Toggle dark mode"
						>
							{#if theme.mode === 'dark'}
								<Sun class="size-4" />
							{:else}
								<Moon class="size-4" />
							{/if}
						</button>
						<button
							onclick={() => auth.logout()}
							class="rounded-md p-2 text-sm text-muted-foreground hover:bg-sidebar-accent hover:text-sidebar-accent-foreground group-data-[collapsible=icon]:hidden"
							aria-label="Sign out"
						>
							<LogOut class="size-4" />
						</button>
					</div>
				</Sidebar.MenuItem>
			</Sidebar.Menu>
		</Sidebar.Footer>

		<Sidebar.Rail />
	</Sidebar.Root>

	<!-- Main content + terminal pane -->
	<Sidebar.Inset>
		<header class="flex h-12 shrink-0 items-center gap-2 border-b px-4">
			<Sidebar.Trigger class="-ml-1" />
		</header>
		<div class="flex flex-1 flex-col overflow-hidden">
			<main class="flex-1 overflow-auto">
				{@render children()}
			</main>
			<TerminalPane />
		</div>
	</Sidebar.Inset>
</Sidebar.Provider>
