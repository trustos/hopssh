<script lang="ts">
	import { onMount } from 'svelte';
	import { audit as auditApi } from '$lib/api/client';
	import type { AuditEntryResponse } from '$lib/types/api';

	let entries = $state<AuditEntryResponse[]>([]);
	let loading = $state(true);
	let error = $state('');

	let now = $state(Math.floor(Date.now() / 1000));

	onMount(() => {
		auditApi.list()
			.then(data => { entries = data; })
			.catch(e => { error = e instanceof Error ? e.message : 'Failed to load audit log'; })
			.finally(() => { loading = false; });
		const interval = setInterval(() => { now = Math.floor(Date.now() / 1000); }, 30_000);
		return () => clearInterval(interval);
	});

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

	{#if loading}
		<div class="space-y-3">
			{#each Array(5) as _}
				<div class="h-12 animate-pulse rounded-lg bg-muted"></div>
			{/each}
		</div>
	{:else if error}
		<div class="rounded-lg border border-destructive/50 bg-destructive/10 p-4 text-sm text-destructive">
			{error}
		</div>
	{:else if entries.length === 0}
		<div class="rounded-lg border border-dashed p-8 text-center">
			<p class="text-sm text-muted-foreground">No audit entries yet. Actions like logins, terminal sessions, and node changes will appear here.</p>
		</div>
	{:else}
		<div class="overflow-x-auto rounded-lg border">
			<table class="w-full text-sm">
				<thead>
					<tr class="border-b bg-muted/50">
						<th class="px-4 py-3 text-left font-medium">Action</th>
						<th class="px-4 py-3 text-left font-medium">User</th>
						<th class="px-4 py-3 text-left font-medium">Info</th>
						<th class="px-4 py-3 text-left font-medium">Node</th>
						<th class="px-4 py-3 text-right font-medium">When</th>
					</tr>
				</thead>
				<tbody>
					{#each entries as entry}
						<tr class="border-b last:border-0 hover:bg-accent/50">
							<td class="px-4 py-3 font-medium">{actionLabel(entry.action)}</td>
							<td class="px-4 py-3 text-muted-foreground">{displayUser(entry)}</td>
							<td class="px-4 py-3 font-mono text-xs text-muted-foreground">
								{entry.details || ''}
							</td>
							<td class="px-4 py-3 font-mono text-xs text-muted-foreground">
								{displayNode(entry)}
							</td>
							<td class="px-4 py-3 text-right text-muted-foreground"
								title={formatTimestamp(entry.createdAt)}>
								{timeAgo(entry.createdAt)}
							</td>
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
</div>
