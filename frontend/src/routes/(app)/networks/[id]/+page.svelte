<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/state';
	import { networks as networksApi, nodes as nodesApi, dns as dnsApi } from '$lib/api/client';
	import { ApiError } from '$lib/api/client';
	import type { NetworkDetailResponse, CreateNodeResponse, DNSRecordResponse } from '$lib/types/api';

	let network = $state<NetworkDetailResponse | null>(null);
	let dnsRecords = $state<DNSRecordResponse[]>([]);
	let loading = $state(true);
	let error = $state('');

	// Add Node dialog
	let showAddNode = $state(false);
	let addingNode = $state(false);
	let nodeResult = $state<CreateNodeResponse | null>(null);
	let addNodeError = $state('');
	let copied = $state(false);

	// Add DNS dialog
	let showAddDNS = $state(false);
	let dnsName = $state('');
	let dnsIP = $state('');
	let addingDNS = $state(false);
	let addDNSError = $state('');

	// Active tab
	let activeTab = $state<'nodes' | 'dns' | 'join'>('nodes');

	// Time ticker for reactive timeAgo
	let now = $state(Math.floor(Date.now() / 1000));

	const networkId = $derived(page.params.id);

	onMount(async () => {
		await loadNetwork();
		const interval = setInterval(() => {
			now = Math.floor(Date.now() / 1000);
		}, 30_000);
		return () => clearInterval(interval);
	});

	async function loadNetwork() {
		loading = true;
		error = '';
		try {
			network = await networksApi.get(networkId);
			dnsRecords = await dnsApi.list(networkId);
		} catch (e) {
			error = e instanceof Error ? e.message : 'Failed to load network';
		} finally {
			loading = false;
		}
	}

	async function addNode() {
		addingNode = true;
		addNodeError = '';
		nodeResult = null;
		try {
			nodeResult = await nodesApi.create(networkId);
		} catch (e) {
			addNodeError = e instanceof ApiError ? e.message : 'Failed to create node';
		} finally {
			addingNode = false;
		}
	}

	function copyCommand() {
		if (!nodeResult) return;
		navigator.clipboard.writeText(nodeResult.installCommand);
		copied = true;
		setTimeout(() => (copied = false), 2000);
	}

	function closeAddNode() {
		const hadResult = !!nodeResult;
		showAddNode = false;
		nodeResult = null;
		addNodeError = '';
		if (hadResult) loadNetwork();
	}

	async function addDNSRecord(e: Event) {
		e.preventDefault();
		addingDNS = true;
		addDNSError = '';
		try {
			await dnsApi.create(networkId, dnsName.trim(), dnsIP.trim());
			showAddDNS = false;
			dnsName = '';
			dnsIP = '';
			dnsRecords = await dnsApi.list(networkId);
		} catch (e) {
			addDNSError = e instanceof ApiError ? e.message : 'Failed to create DNS record';
		} finally {
			addingDNS = false;
		}
	}

	async function deleteDNS(recordId: string) {
		try {
			await dnsApi.delete(networkId, recordId);
			dnsRecords = await dnsApi.list(networkId);
		} catch {
			/* ignore */
		}
	}

	function statusColor(status: string) {
		switch (status) {
			case 'online': return 'bg-primary animate-hop-pulse';
			case 'enrolled': return 'bg-yellow-500';
			case 'offline': return 'bg-gray-500';
			default: return 'border border-dashed border-muted-foreground';
		}
	}

	function timeAgo(ts: number | null): string {
		if (!ts) return 'Never';
		const diff = now - ts;
		if (diff < 60) return 'Just now';
		if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
		if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
		return `${Math.floor(diff / 86400)}d ago`;
	}

	function nodeTypeLabel(t: string) {
		switch (t) {
			case 'agent': return 'Server';
			case 'user': return 'Client';
			case 'lighthouse': return 'Lighthouse';
			default: return t;
		}
	}
</script>

<svelte:head>
	<title>{network?.name ?? 'Network'} - hopssh</title>
</svelte:head>

<div class="p-6">
	{#if loading}
		<div class="mb-6 h-8 w-48 animate-pulse rounded bg-muted"></div>
		<div class="space-y-3">
			{#each Array(3) as _}
				<div class="h-16 animate-pulse rounded-lg bg-muted"></div>
			{/each}
		</div>
	{:else if error}
		<div class="rounded-lg border border-destructive/50 bg-destructive/10 p-4 text-sm text-destructive">
			{error}
		</div>
	{:else if network}
		<!-- Header -->
		<div class="mb-6 flex items-center justify-between">
			<div>
				<h1 class="text-2xl font-semibold">{network.name}</h1>
				<div class="flex gap-3 text-sm text-muted-foreground">
					<span class="font-mono">{network.subnet}</span>
					<span>DNS: <span class="font-mono">.{network.dnsDomain}</span></span>
				</div>
			</div>
			<button
				onclick={() => { showAddNode = true; addNode(); }}
				class="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90"
			>
				Add Node
			</button>
		</div>

		<!-- Tabs -->
		<div class="mb-4 flex gap-1 border-b">
			<button
				class="px-4 py-2 text-sm font-medium {activeTab === 'nodes' ? 'border-b-2 border-primary text-primary' : 'text-muted-foreground hover:text-foreground'}"
				onclick={() => (activeTab = 'nodes')}
			>
				Nodes ({network.nodes.length})
			</button>
			<button
				class="px-4 py-2 text-sm font-medium {activeTab === 'dns' ? 'border-b-2 border-primary text-primary' : 'text-muted-foreground hover:text-foreground'}"
				onclick={() => (activeTab = 'dns')}
			>
				DNS ({dnsRecords.length})
			</button>
			<button
				class="px-4 py-2 text-sm font-medium {activeTab === 'join' ? 'border-b-2 border-primary text-primary' : 'text-muted-foreground hover:text-foreground'}"
				onclick={() => (activeTab = 'join')}
			>
				Join
			</button>
		</div>

		<!-- Nodes Tab -->
		{#if activeTab === 'nodes'}
			{#if network.nodes.length === 0}
				<div class="rounded-lg border border-dashed p-8 text-center">
					<p class="mb-2 text-lg font-medium">No nodes yet</p>
					<p class="text-sm text-muted-foreground">
						Add a node to get started.
					</p>
				</div>
			{:else}
				<div class="rounded-lg border">
					<table class="w-full text-sm">
						<thead>
							<tr class="border-b bg-muted/50">
								<th class="px-4 py-3 text-left font-medium">Status</th>
								<th class="px-4 py-3 text-left font-medium">Name</th>
								<th class="px-4 py-3 text-left font-medium">Type</th>
								<th class="px-4 py-3 text-left font-medium">IP</th>
								<th class="px-4 py-3 text-left font-medium">DNS</th>
								<th class="px-4 py-3 text-left font-medium">Last Seen</th>
								<th class="px-4 py-3 text-left font-medium">Actions</th>
							</tr>
						</thead>
						<tbody>
							{#each network.nodes as node}
								<tr class="border-b last:border-0 hover:bg-accent/50">
									<td class="px-4 py-3">
										<div class="flex items-center gap-2">
											<div class="h-2.5 w-2.5 rounded-full {statusColor(node.status)}"></div>
											<span class="text-xs capitalize text-muted-foreground">{node.status}</span>
										</div>
									</td>
									<td class="px-4 py-3">
										{#if node.nodeType === 'agent'}
											<a
												href="/terminal/{networkId}/{node.id}"
												class="font-mono font-medium text-primary hover:underline"
											>
												{node.hostname || node.id.slice(0, 8)}
											</a>
										{:else}
											<span class="font-mono font-medium">{node.hostname || node.id.slice(0, 8)}</span>
										{/if}
									</td>
									<td class="px-4 py-3">
										<span class="rounded-full bg-muted px-2 py-0.5 text-xs">{nodeTypeLabel(node.nodeType)}</span>
									</td>
									<td class="px-4 py-3 font-mono text-muted-foreground">{node.nebulaIP}</td>
									<td class="px-4 py-3 font-mono text-muted-foreground text-xs">
										{#if node.dnsName || node.hostname}
											{node.dnsName || node.hostname}.{network.dnsDomain}
										{:else}
											<span class="text-muted-foreground/50">—</span>
										{/if}
									</td>
									<td class="px-4 py-3 text-muted-foreground">{timeAgo(node.lastSeenAt)}</td>
									<td class="px-4 py-3">
										{#if node.nodeType === 'agent'}
											<a
												href="/terminal/{networkId}/{node.id}"
												class="rounded px-2 py-1 text-xs font-medium text-primary hover:bg-primary/10"
											>
												Terminal
											</a>
										{/if}
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{/if}

		<!-- DNS Tab -->
		{:else if activeTab === 'dns'}
			<div class="mb-4 flex items-center justify-between">
				<p class="text-sm text-muted-foreground">
					Custom DNS records for <span class="font-mono">.{network.dnsDomain}</span>.
					Node hostnames are resolved automatically.
				</p>
				<button
					onclick={() => (showAddDNS = true)}
					class="rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground hover:bg-primary/90"
				>
					Add Record
				</button>
			</div>

			{#if dnsRecords.length === 0}
				<div class="rounded-lg border border-dashed p-6 text-center">
					<p class="text-sm text-muted-foreground">No custom DNS records. Node hostnames are resolved automatically.</p>
				</div>
			{:else}
				<div class="rounded-lg border">
					<table class="w-full text-sm">
						<thead>
							<tr class="border-b bg-muted/50">
								<th class="px-4 py-3 text-left font-medium">Hostname</th>
								<th class="px-4 py-3 text-left font-medium">Resolves to</th>
								<th class="px-4 py-3 text-left font-medium">FQDN</th>
								<th class="px-4 py-3 text-left font-medium"></th>
							</tr>
						</thead>
						<tbody>
							{#each dnsRecords as record}
								<tr class="border-b last:border-0 hover:bg-accent/50">
									<td class="px-4 py-3 font-mono font-medium">{record.name}</td>
									<td class="px-4 py-3 font-mono text-muted-foreground">{record.nebulaIP}</td>
									<td class="px-4 py-3 font-mono text-xs text-muted-foreground">{record.name}.{network.dnsDomain}</td>
									<td class="px-4 py-3 text-right">
										<button
											onclick={() => deleteDNS(record.id)}
											class="rounded px-2 py-1 text-xs text-destructive hover:bg-destructive/10"
										>
											Delete
										</button>
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{/if}

		<!-- Join Tab -->
		{:else if activeTab === 'join'}
			<div class="max-w-lg space-y-6">
				<div>
					<h3 class="mb-2 font-medium">Join from a laptop or phone</h3>
					<p class="mb-3 text-sm text-muted-foreground">
						Connect your personal device to this network to access services like
						Jellyfin, Immich, or Paperless by name.
					</p>
					<div class="rounded-md bg-muted p-3">
						<pre class="font-mono text-xs">hop-agent client join --network {networkId} --endpoint {window.location.origin}</pre>
					</div>
					<p class="mt-2 text-xs text-muted-foreground">
						After joining, services are reachable as <span class="font-mono">hostname.{network.dnsDomain}</span>
					</p>
				</div>

				<div>
					<h3 class="mb-2 font-medium">Add a server</h3>
					<p class="mb-3 text-sm text-muted-foreground">
						Install the agent on a server to manage it from the dashboard and make
						its services available on the mesh.
					</p>
					<button
						onclick={() => { showAddNode = true; addNode(); }}
						class="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90"
					>
						Add Node
					</button>
				</div>
			</div>
		{/if}

		<!-- Add Node Dialog -->
		{#if showAddNode}
			<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
				<div class="w-full max-w-md rounded-lg border bg-card p-6 shadow-lg">
					<h2 class="mb-4 text-lg font-semibold">Add Node</h2>
					{#if addingNode}
						<div class="flex items-center gap-3 py-4">
							<div class="h-5 w-5 animate-spin rounded-full border-2 border-primary border-t-transparent"></div>
							<span class="text-sm text-muted-foreground">Generating enrollment token...</span>
						</div>
					{:else if addNodeError}
						<div class="mb-4 rounded-md bg-destructive/10 p-3 text-sm text-destructive">{addNodeError}</div>
					{:else if nodeResult}
						<div class="space-y-4">
							<div>
								<p class="mb-2 text-sm text-muted-foreground">Run this on your server:</p>
								<div class="relative">
									<pre class="overflow-x-auto rounded-md bg-muted p-3 font-mono text-xs">{nodeResult.installCommand}</pre>
									<button
										onclick={copyCommand}
										class="absolute right-2 top-2 rounded px-2 py-1 text-xs hover:bg-accent"
									>
										{copied ? 'Copied!' : 'Copy'}
									</button>
								</div>
							</div>
							<div class="text-xs text-muted-foreground">
								<p>Token expires in 10 minutes. IP: <span class="font-mono">{nodeResult.nebulaIP}</span></p>
							</div>
						</div>
					{/if}
					<div class="mt-4 flex justify-end">
						<button onclick={closeAddNode} class="rounded-md px-4 py-2 text-sm hover:bg-accent">
							{nodeResult ? 'Done' : 'Cancel'}
						</button>
					</div>
				</div>
			</div>
		{/if}

		<!-- Add DNS Dialog -->
		{#if showAddDNS}
			<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
				<div class="w-full max-w-sm rounded-lg border bg-card p-6 shadow-lg">
					<h2 class="mb-4 text-lg font-semibold">Add DNS Record</h2>
					<form onsubmit={addDNSRecord} class="space-y-4">
						{#if addDNSError}
							<div class="rounded-md bg-destructive/10 p-3 text-sm text-destructive">{addDNSError}</div>
						{/if}
						<div class="space-y-2">
							<label for="dns-name" class="text-sm font-medium">Hostname</label>
							<div class="flex items-center gap-1">
								<input
									id="dns-name"
									type="text"
									bind:value={dnsName}
									required
									placeholder="jellyfin"
									class="w-full rounded-md border bg-background px-3 py-2 font-mono text-sm focus:outline-none focus:ring-2 focus:ring-ring"
								/>
								<span class="text-sm text-muted-foreground">.{network.dnsDomain}</span>
							</div>
						</div>
						<div class="space-y-2">
							<label for="dns-ip" class="text-sm font-medium">VPN IP</label>
							<input
								id="dns-ip"
								type="text"
								bind:value={dnsIP}
								required
								placeholder="10.42.1.3"
								class="w-full rounded-md border bg-background px-3 py-2 font-mono text-sm focus:outline-none focus:ring-2 focus:ring-ring"
							/>
						</div>
						<div class="flex justify-end gap-2">
							<button
								type="button"
								onclick={() => { showAddDNS = false; addDNSError = ''; }}
								class="rounded-md px-4 py-2 text-sm hover:bg-accent"
							>
								Cancel
							</button>
							<button
								type="submit"
								disabled={addingDNS || !dnsName.trim() || !dnsIP.trim()}
								class="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
							>
								{addingDNS ? 'Creating...' : 'Create'}
							</button>
						</div>
					</form>
				</div>
			</div>
		{/if}
	{/if}
</div>
