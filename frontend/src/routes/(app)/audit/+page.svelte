<script lang="ts">
	import { onMount } from 'svelte';
	import { audit as auditApi } from '$lib/api/client';
	import type { AuditEntryResponse } from '$lib/types/api';
	import * as Table from '$lib/components/ui/table/index.js';
	import * as Alert from '$lib/components/ui/alert/index.js';
	import * as Tooltip from '$lib/components/ui/tooltip/index.js';
	import { Badge } from '$lib/components/ui/badge/index.js';
	import { Button } from '$lib/components/ui/button/index.js';
	import { Input } from '$lib/components/ui/input/index.js';
	import { Skeleton } from '$lib/components/ui/skeleton/index.js';

	let entries = $state<AuditEntryResponse[]>([]);
	let loading = $state(true);
	let error = $state('');
	let search = $state('');

	// Time window buttons → unix seconds cutoff. Matches the activity log
	// conventions (server defaults to now-24h; explicit 0 = all).
	type RangeKey = '24h' | '7d' | '30d' | 'all';
	let range = $state<RangeKey>('24h');
	function rangeSince(k: RangeKey): number {
		const n = Math.floor(Date.now() / 1000);
		switch (k) {
			case '24h': return n - 86400;
			case '7d':  return n - 86400 * 7;
			case '30d': return n - 86400 * 30;
			case 'all': return 0;
		}
	}

	let now = $state(Math.floor(Date.now() / 1000));

	async function reload() {
		loading = true;
		error = '';
		try {
			entries = await auditApi.list({ since: rangeSince(range), limit: 500 });
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to load audit log';
		} finally {
			loading = false;
		}
	}

	onMount(() => {
		reload();
		const interval = setInterval(() => { now = Math.floor(Date.now() / 1000); }, 30_000);
		return () => clearInterval(interval);
	});

	// Refetch when the range changes.
	$effect(() => {
		void range;
		reload();
	});

	// Client-side search across action + user + hostname + details.
	const filtered = $derived(entries.filter(e => {
		if (!search) return true;
		const q = search.toLowerCase();
		return [
			e.action, e.userEmail ?? '', e.userName ?? '',
			e.nodeHostname ?? '', e.details ?? '',
		].join(' ').toLowerCase().includes(q);
	}));

	function timeAgo(ts: number): string {
		const diff = now - ts;
		if (diff < 60) return 'Just now';
		if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
		if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
		return `${Math.floor(diff / 86400)}d ago`;
	}

	function formatTimestamp(ts: number): string {
		return new Date(ts * 1000).toLocaleString();
	}

	function actionLabel(action: string): string {
		const labels: Record<string, string> = {
			'login': 'Logged in',
			'register': 'Registered',
			'shell.connect': 'Terminal session',
			'exec': 'Command exec',
			'port_forward.start': 'Port forward started',
			'port_forward.proxy': 'Proxy access',
			'node.delete': 'Node deleted',
			'node.capabilities': 'Capabilities updated',
			'node.rename': 'Node renamed',
		};
		return labels[action] || action;
	}

	function actionVariant(action: string): 'default' | 'secondary' | 'destructive' | 'outline' {
		if (action === 'node.delete') return 'destructive';
		if (action.startsWith('shell') || action.startsWith('exec') || action.startsWith('port_forward')) return 'default';
		return 'secondary';
	}

	function displayUser(entry: AuditEntryResponse): string {
		return entry.userName || entry.userEmail || entry.userId.slice(0, 8);
	}

	function displayNode(entry: AuditEntryResponse): string {
		if (entry.nodeHostname) return entry.nodeHostname;
		if (entry.nodeId) return entry.nodeId.slice(0, 8);
		return '';
	}
</script>

<svelte:head>
	<title>Audit Log - hopssh</title>
</svelte:head>

<div class="p-6">
	<h1 class="mb-6 text-2xl font-semibold">Audit Log</h1>

	<div class="mb-4 flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
		<div class="flex gap-1">
			{#each ['24h', '7d', '30d', 'all'] as r (r)}
				<Button
					size="sm"
					variant={range === r ? 'default' : 'outline'}
					onclick={() => (range = r as RangeKey)}
				>{r}</Button>
			{/each}
		</div>
		<Input
			type="search"
			placeholder="Search"
			class="h-8 w-full sm:w-48"
			bind:value={search}
		/>
	</div>

	{#if loading}
		<div class="space-y-3">
			{#each Array(5) as _}
				<Skeleton class="h-12 w-full rounded-lg" />
			{/each}
		</div>
	{:else if error}
		<Alert.Root variant="destructive">
			<Alert.Description>{error}</Alert.Description>
		</Alert.Root>
	{:else if entries.length === 0}
		<div class="rounded-lg border border-dashed p-8 text-center">
			<p class="text-sm text-muted-foreground">No audit entries yet. Actions like logins, terminal sessions, and node changes will appear here.</p>
		</div>
	{:else if filtered.length === 0}
		<div class="rounded-lg border border-dashed p-8 text-center">
			<p class="text-sm text-muted-foreground">No entries match the current search.</p>
		</div>
	{:else}
		<div class="rounded-lg border overflow-hidden">
			<Table.Root>
				<Table.Header>
					<Table.Row>
						<Table.Head>Action</Table.Head>
						<Table.Head class="hidden sm:table-cell">User</Table.Head>
						<Table.Head class="hidden lg:table-cell">Info</Table.Head>
						<Table.Head class="hidden md:table-cell">Node</Table.Head>
						<Table.Head class="text-right">When</Table.Head>
					</Table.Row>
				</Table.Header>
				<Table.Body>
					{#each filtered as entry}
						<Table.Row>
							<Table.Cell>
								<Badge variant={actionVariant(entry.action)}>
									{actionLabel(entry.action)}
								</Badge>
							</Table.Cell>
							<Table.Cell class="hidden sm:table-cell text-muted-foreground">{displayUser(entry)}</Table.Cell>
							<Table.Cell class="hidden lg:table-cell max-w-[200px] truncate font-mono text-xs text-muted-foreground">
								{entry.details || ''}
							</Table.Cell>
							<Table.Cell class="hidden md:table-cell font-mono text-xs text-muted-foreground">
								{displayNode(entry)}
							</Table.Cell>
							<Table.Cell class="text-right">
								<Tooltip.Root>
									<Tooltip.Trigger class="text-muted-foreground">
										{timeAgo(entry.createdAt)}
									</Tooltip.Trigger>
									<Tooltip.Content>
										{formatTimestamp(entry.createdAt)}
									</Tooltip.Content>
								</Tooltip.Root>
							</Table.Cell>
						</Table.Row>
					{/each}
				</Table.Body>
			</Table.Root>
		</div>
	{/if}
</div>
