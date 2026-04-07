<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';
	import { toast } from 'svelte-sonner';
	import { networks as networksApi, nodes as nodesApi, dns as dnsApi, portForwards as fwdApi, members as membersApi, invites as invitesApi } from '$lib/api/client';
	import { ApiError } from '$lib/api/client';
	import type { NetworkDetailResponse, CreateNodeResponse, DNSRecordResponse, NodeResponse, PortForwardResponse, NetworkMemberResponse, InviteResponse } from '$lib/types/api';
	import Dialog from '$lib/components/ui/dialog.svelte';

	let network = $state<NetworkDetailResponse | null>(null);
	let dnsRecords = $state<DNSRecordResponse[]>([]);
	let networkMembers = $state<NetworkMemberResponse[]>([]);
	let networkInvites = $state<InviteResponse[]>([]);
	let loading = $state(true);
	let error = $state('');

	// Add Node dialog
	let showAddNode = $state(false);
	let addingNode = $state(false);
	let nodeResult = $state<CreateNodeResponse | null>(null);
	let addNodeError = $state('');

	// Add DNS dialog
	let showAddDNS = $state(false);
	let dnsName = $state('');
	let dnsIP = $state('');
	let addingDNS = $state(false);
	let addDNSError = $state('');

	// Delete network
	let showDeleteNetwork = $state(false);
	let deleteNetworkConfirm = $state('');
	let deletingNetwork = $state(false);
	let deleteNetworkError = $state('');

	// Port forwards
	let activeForwards = $state<PortForwardResponse[]>([]);
	let forwardNodeId = $state<string | null>(null); // which node's inline form is open
	let fwdRemotePort = $state('');
	let fwdLocalPort = $state('');
	let startingForward = $state(false);

	// Delete node
	let showDeleteNode = $state(false);
	let nodeToDelete = $state<NodeResponse | null>(null);
	let deletingNode = $state(false);
	let deleteNodeError = $state('');

	// Active tab — default to "join" for new networks, "nodes" once there are nodes
	let activeTab = $state<'nodes' | 'dns' | 'join' | 'members'>('nodes');
	let initialTabSet = $state(false);

	// Rename node
	let renamingNodeId = $state<string | null>(null);
	let renameValue = $state('');

	// Create invite dialog
	let showCreateInvite = $state(false);
	let inviteExpiresIn = $state<string>('86400');
	let inviteMaxUses = $state<string>('');
	let creatingInvite = $state(false);
	let inviteResult = $state<{ code: string } | null>(null);

	// Role-based access
	const isAdmin = $derived(network?.role === 'admin');

	// Install command using the control plane's install script
	const installScriptCmd = $derived(`curl -fsSL ${window.location.origin}/install.sh | sh`);

	const enrollCommand = $derived.by(() => {
		if (!nodeResult) return '';
		const token = nodeResult.enrollmentToken;
		const endpoint = nodeResult.endpoint;
		return `echo '${token}' | sudo hop-agent enroll --token-stdin --endpoint ${endpoint}`;
	});

	// Time ticker for reactive timeAgo
	let now = $state(Math.floor(Date.now() / 1000));

	const networkId = $derived(page.params.id!);

	// All nodes including pending (pending shown with special style).
	const visibleNodes = $derived(network?.nodes ?? []);

	const hasPendingNodes = $derived(network?.nodes.some(n => n.status === 'pending') ?? false);

	onMount(() => {
		// Tick for timeAgo display
		const tickInterval = setInterval(() => {
			now = Math.floor(Date.now() / 1000);
		}, 30_000);

		// WebSocket for real-time events with polling fallback.
		let ws: WebSocket | null = null;
		let wsRetryTimer: ReturnType<typeof setTimeout> | null = null;

		function connectEvents() {
			if (!networkId) return;
			const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
			ws = new WebSocket(`${proto}//${window.location.host}/api/networks/${networkId}/events`);
			ws.onmessage = () => {
				loadNetwork();
			};
			ws.onclose = () => {
				ws = null;
				wsRetryTimer = setTimeout(connectEvents, 30_000);
			};
			ws.onerror = () => {
				ws?.close();
			};
		}

		// Load network first, then connect WebSocket.
		loadNetwork().then(() => connectEvents());

		// Fallback poll every 15s.
		const pollInterval = setInterval(() => {
			if (!ws || ws.readyState !== WebSocket.OPEN || hasPendingNodes || showAddNode) {
				loadNetwork();
			}
		}, 15_000);

		return () => {
			clearInterval(tickInterval);
			clearInterval(pollInterval);
			if (wsRetryTimer) clearTimeout(wsRetryTimer);
			if (ws) ws.close();
		};
	});

	async function loadNetwork() {
		loading = true;
		error = '';
		try {
			network = await networksApi.get(networkId);
			dnsRecords = await dnsApi.list(networkId);
			activeForwards = await fwdApi.list(networkId).catch(() => []);
			networkMembers = await membersApi.list(networkId).catch(() => []);
			if (network.role === 'admin') {
				networkInvites = await invitesApi.list(networkId).catch(() => []);
			}
			// Default to "join" tab for empty networks on first load
			if (!initialTabSet) {
				initialTabSet = true;
				if (network.nodes.length === 0) {
					activeTab = 'join';
				}
			}
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

	function copyToClipboard(text: string, label = 'Copied to clipboard') {
		navigator.clipboard.writeText(text);
		toast.success(label);
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

	async function deleteNetwork() {
		if (!network || deleteNetworkConfirm !== network.name) return;
		deletingNetwork = true;
		deleteNetworkError = '';
		try {
			await networksApi.delete(networkId);
			goto('/');
		} catch (e) {
			deleteNetworkError = e instanceof ApiError ? e.message : 'Failed to delete network';
		} finally {
			deletingNetwork = false;
		}
	}

	function confirmDeleteNode(node: NodeResponse) {
		nodeToDelete = node;
		showDeleteNode = true;
		deleteNodeError = '';
	}

	async function deleteNode() {
		if (!nodeToDelete) return;
		deletingNode = true;
		deleteNodeError = '';
		try {
			await nodesApi.delete(networkId, nodeToDelete.id);
			showDeleteNode = false;
			nodeToDelete = null;
			await loadNetwork();
		} catch (e) {
			deleteNodeError = e instanceof ApiError ? e.message : 'Failed to delete node';
		} finally {
			deletingNode = false;
		}
	}

	async function checkHealth(node: NodeResponse) {
		try {
			const h = await nodesApi.health(networkId, node.id);
			toast.success(`${node.hostname || node.id.slice(0, 8)}: ${h.status} — uptime ${h.uptime}`);
			// Refresh to update last_seen
			await loadNetwork();
		} catch (e) {
			toast.error(`Health check failed: ${e instanceof ApiError ? e.message : 'unreachable'}`);
		}
	}

	async function startForward(nodeId: string, e: Event) {
		e.preventDefault();
		const remote = parseInt(fwdRemotePort);
		if (!remote || remote < 1 || remote > 65535) return;
		const local = fwdLocalPort ? parseInt(fwdLocalPort) : undefined;
		startingForward = true;
		try {
			const pf = await fwdApi.start(networkId, nodeId, remote, local);
			activeForwards = [...activeForwards, pf];
			forwardNodeId = null;
			fwdRemotePort = '';
			fwdLocalPort = '';
			const addr = `localhost:${pf.localPort}`;
			navigator.clipboard.writeText(addr).catch(() => {});
			toast.success(`Forwarding ${addr} — copied to clipboard`);
		} catch (e) {
			toast.error(e instanceof ApiError ? e.message : 'Failed to start forward');
		} finally {
			startingForward = false;
		}
	}

	async function stopForward(fwdId: string) {
		try {
			await fwdApi.stop(networkId, fwdId);
			activeForwards = activeForwards.filter(f => f.id !== fwdId);
			toast.success('Port forward stopped');
		} catch (e) {
			toast.error(e instanceof ApiError ? e.message : 'Failed to stop forward');
		}
	}

	function copyAddr(port: number) {
		navigator.clipboard.writeText(`localhost:${port}`);
		toast.success('Copied to clipboard');
	}

	function nodeHostname(nodeId: string): string {
		const node = network?.nodes.find(n => n.id === nodeId);
		return node?.hostname || nodeId.slice(0, 8);
	}

	async function deleteDNS(recordId: string) {
		try {
			await dnsApi.delete(networkId, recordId);
			dnsRecords = await dnsApi.list(networkId);
			toast.success('DNS record deleted');
		} catch (e) {
			toast.error(e instanceof ApiError ? e.message : 'Failed to delete DNS record');
		}
	}

	async function createInvite(ev: Event) {
		ev.preventDefault();
		creatingInvite = true;
		try {
			const opts: { maxUses?: number; expiresIn?: number } = {};
			if (inviteExpiresIn && inviteExpiresIn !== '0') opts.expiresIn = parseInt(inviteExpiresIn);
			if (inviteMaxUses) opts.maxUses = parseInt(inviteMaxUses);
			const result = await invitesApi.create(networkId, opts);
			inviteResult = { code: result.code };
			networkInvites = await invitesApi.list(networkId).catch(() => []);
		} catch (e) {
			toast.error(e instanceof ApiError ? e.message : 'Failed to create invite');
		} finally {
			creatingInvite = false;
		}
	}

	function copyInviteLink(code: string) {
		const url = `${window.location.origin}/invite/${code}`;
		navigator.clipboard.writeText(url);
		toast.success('Invite link copied');
	}

	async function revokeInvite(inviteId: string) {
		try {
			await invitesApi.delete(networkId, inviteId);
			networkInvites = networkInvites.filter(i => i.id !== inviteId);
			toast.success('Invite revoked');
		} catch (e) {
			toast.error(e instanceof ApiError ? e.message : 'Failed to revoke invite');
		}
	}

	async function removeMember(memberId: string) {
		try {
			await membersApi.remove(networkId, memberId);
			networkMembers = networkMembers.filter(m => m.id !== memberId);
			toast.success('Member removed');
		} catch (e) {
			toast.error(e instanceof ApiError ? e.message : 'Failed to remove member');
		}
	}

	async function renameNode(nodeId: string) {
		const name = renameValue.trim();
		if (!name) return;
		try {
			await nodesApi.rename(networkId, nodeId, name);
			renamingNodeId = null;
			renameValue = '';
			await loadNetwork();
			toast.success('Node renamed');
		} catch (e) {
			toast.error(e instanceof ApiError ? e.message : 'Failed to rename node');
		}
	}

	function statusColor(status: string) {
		switch (status) {
			case 'online': return 'bg-primary animate-hop-pulse';
			case 'enrolled': return 'bg-yellow-500';
			case 'offline': return 'bg-gray-500';
			case 'pending': return 'border-2 border-dashed border-yellow-500 animate-pulse';
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
			{#if isAdmin}
				<button
					onclick={() => { showDeleteNetwork = true; deleteNetworkConfirm = ''; deleteNetworkError = ''; }}
					class="rounded-md px-3 py-2 text-sm text-destructive hover:bg-destructive/10"
				>
					Delete Network
				</button>
			{/if}
		</div>

		<!-- Tabs -->
		<div class="mb-4 flex gap-1 border-b">
			<button
				class="px-4 py-2 text-sm font-medium {activeTab === 'join' ? 'border-b-2 border-primary text-primary' : 'text-muted-foreground hover:text-foreground'}"
				onclick={() => (activeTab = 'join')}
			>
				Join
			</button>
			<button
				class="px-4 py-2 text-sm font-medium {activeTab === 'nodes' ? 'border-b-2 border-primary text-primary' : 'text-muted-foreground hover:text-foreground'}"
				onclick={() => (activeTab = 'nodes')}
			>
				Nodes ({visibleNodes.length})
			</button>
			<button
				class="px-4 py-2 text-sm font-medium {activeTab === 'dns' ? 'border-b-2 border-primary text-primary' : 'text-muted-foreground hover:text-foreground'}"
				onclick={() => (activeTab = 'dns')}
			>
				DNS
			</button>
			<button
				class="px-4 py-2 text-sm font-medium {activeTab === 'members' ? 'border-b-2 border-primary text-primary' : 'text-muted-foreground hover:text-foreground'}"
				onclick={() => (activeTab = 'members')}
			>
				Members ({networkMembers.length})
			</button>
		</div>

		<!-- Nodes Tab -->
		{#if activeTab === 'nodes'}
			<!-- Active Forwards -->
			{#if activeForwards.length > 0}
				<div class="mb-4 rounded-lg border bg-primary/5 p-3">
					<div class="mb-2 text-xs font-medium uppercase tracking-wider text-muted-foreground">
						Active Forwards
					</div>
					{#each activeForwards as fwd}
						<div class="flex items-center justify-between border-b border-primary/10 py-1.5 last:border-0 font-mono text-sm">
							<span>
								<span class="text-muted-foreground">{nodeHostname(fwd.nodeId)}</span>
								<span class="text-muted-foreground">:</span>{fwd.remotePort}
								<span class="text-muted-foreground mx-1">→</span>
								localhost:{fwd.localPort}
							</span>
							<div class="flex gap-1">
								<button
									onclick={() => copyAddr(fwd.localPort)}
									class="rounded px-2 py-0.5 text-xs text-muted-foreground hover:text-foreground"
								>
									Copy
								</button>
								<button
									onclick={() => stopForward(fwd.id)}
									class="rounded px-2 py-0.5 text-xs text-destructive hover:bg-destructive/10"
								>
									Stop
								</button>
							</div>
						</div>
					{/each}
				</div>
			{/if}

			{#if visibleNodes.length === 0}
				<div class="rounded-lg border border-dashed p-8 text-center">
					<p class="mb-2 text-lg font-medium">No nodes yet</p>
					<p class="text-sm text-muted-foreground">
						Add a server to get started.
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
							{#each visibleNodes as node}
								<tr class="border-b last:border-0 hover:bg-accent/50">
									<td class="px-4 py-3">
										<div class="flex items-center gap-2" title={node.status === 'pending' ? 'Waiting for agent enrollment. Run the install command on your server.' : ''}>
											<div class="h-2.5 w-2.5 rounded-full {statusColor(node.status)}"></div>
											<span class="text-xs capitalize text-muted-foreground">{node.status}</span>
											{#if node.status === 'pending'}
												<span class="text-xs text-yellow-500">awaiting enrollment</span>
											{/if}
										</div>
									</td>
									<td class="px-4 py-3">
										{#if renamingNodeId === node.id}
											<form onsubmit={(e) => { e.preventDefault(); renameNode(node.id); }} class="flex items-center gap-1">
												<input
													type="text"
													bind:value={renameValue}
													class="w-32 rounded border bg-background px-2 py-0.5 font-mono text-sm focus:outline-none focus:ring-1 focus:ring-ring"
													autofocus
												/>
												<button type="submit" class="rounded px-1.5 py-0.5 text-xs text-primary hover:bg-primary/10">Save</button>
												<button type="button" onclick={() => { renamingNodeId = null; }} class="rounded px-1.5 py-0.5 text-xs text-muted-foreground hover:text-foreground">Cancel</button>
											</form>
										{:else}
											<span class="group flex items-center gap-1">
												{#if node.nodeType === 'agent'}
													<a
														href="/terminal/{networkId}/{node.id}?h={encodeURIComponent(node.hostname || node.id.slice(0, 8))}"
														class="font-mono font-medium text-primary hover:underline"
													>
														{node.dnsName || node.hostname || node.id.slice(0, 8)}
													</a>
												{:else}
													<span class="font-mono font-medium">{node.dnsName || node.hostname || node.id.slice(0, 8)}</span>
												{/if}
												{#if isAdmin}
													<button
														onclick={() => { renamingNodeId = node.id; renameValue = node.dnsName || node.hostname || ''; }}
														class="invisible rounded px-1 text-xs text-muted-foreground hover:text-foreground group-hover:visible"
														title="Rename"
													>
														&#9998;
													</button>
												{/if}
											</span>
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
										<div class="flex gap-1">
											{#if node.nodeType === 'agent' && node.status !== 'pending'}
												<button
													onclick={() => checkHealth(node)}
													class="rounded px-2 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground"
												>
													Health
												</button>
												<a
													href="/terminal/{networkId}/{node.id}?h={encodeURIComponent(node.hostname || node.id.slice(0, 8))}"
													class="rounded px-2 py-1 text-xs font-medium text-primary hover:bg-primary/10"
												>
													Terminal
												</a>
											{/if}
											{#if node.nodeType === 'agent' && node.status === 'online'}
												<button
													onclick={() => { forwardNodeId = forwardNodeId === node.id ? null : node.id; fwdRemotePort = ''; fwdLocalPort = ''; }}
													class="rounded px-2 py-1 text-xs text-primary hover:bg-primary/10"
												>
													Forward
												</button>
											{/if}
											{#if node.nodeType !== 'lighthouse'}
												<button
													onclick={() => confirmDeleteNode(node)}
													class="rounded px-2 py-1 text-xs text-destructive hover:bg-destructive/10"
												>
													Delete
												</button>
											{/if}
										</div>
									</td>
								</tr>
								<!-- Inline port forward form -->
								{#if forwardNodeId === node.id}
									<tr class="bg-muted/50">
										<td colspan="7" class="px-4 py-3">
											<form onsubmit={(e) => startForward(node.id, e)} class="flex items-center gap-3">
												<span class="text-sm text-muted-foreground">Forward port from {node.hostname || node.id.slice(0, 8)}:</span>
												<div class="flex items-center gap-1">
													<span class="text-xs text-muted-foreground">Remote</span>
													<input
														type="number"
														bind:value={fwdRemotePort}
														min="1"
														max="65535"
														required
														placeholder="5432"
														class="w-20 rounded border bg-background px-2 py-1 font-mono text-sm focus:outline-none focus:ring-1 focus:ring-ring"
													/>
												</div>
												<div class="flex items-center gap-1">
													<span class="text-xs text-muted-foreground">Local</span>
													<input
														type="number"
														bind:value={fwdLocalPort}
														min="0"
														max="65535"
														placeholder="auto"
														class="w-20 rounded border bg-background px-2 py-1 font-mono text-sm focus:outline-none focus:ring-1 focus:ring-ring"
													/>
												</div>
												<button
													type="submit"
													disabled={startingForward || !fwdRemotePort}
													class="rounded bg-primary px-3 py-1 text-xs font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50"
												>
													{startingForward ? 'Starting...' : 'Start'}
												</button>
												<button
													type="button"
													onclick={() => { forwardNodeId = null; }}
													class="text-xs text-muted-foreground hover:text-foreground"
												>
													Cancel
												</button>
											</form>
										</td>
									</tr>
								{/if}
							{/each}
						</tbody>
					</table>
				</div>
			{/if}

		<!-- DNS Tab -->
		{:else if activeTab === 'dns'}
			<div class="mb-4 flex items-center justify-between">
				<p class="text-sm text-muted-foreground">
					All resolvable names on <span class="font-mono">.{network.dnsDomain}</span>
				</p>
				{#if isAdmin}
					<button
						onclick={() => (showAddDNS = true)}
						class="rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground hover:bg-primary/90"
					>
						Add Record
					</button>
				{/if}
			</div>

			{@const autoRecords = visibleNodes
				.filter(n => n.nebulaIP && n.status !== 'pending' && (n.dnsName || n.hostname))
				.map(n => ({ name: n.dnsName || n.hostname, ip: n.nebulaIP.split('/')[0], source: 'auto' as const, id: n.id }))}
			{@const customRecordsMapped = dnsRecords.map(r => ({ name: r.name, ip: r.nebulaIP, source: 'custom' as const, id: r.id }))}
			{@const allRecords = [...autoRecords, ...customRecordsMapped]}

			{#if allRecords.length === 0}
				<div class="rounded-lg border border-dashed p-6 text-center">
					<p class="mb-1 text-sm text-muted-foreground">No DNS records yet.</p>
					<p class="text-xs text-muted-foreground">Node hostnames are added automatically when agents enroll. You can also add custom records.</p>
				</div>
			{:else}
				<div class="rounded-lg border">
					<table class="w-full text-sm">
						<thead>
							<tr class="border-b bg-muted/50">
								<th class="px-4 py-3 text-left font-medium">Hostname</th>
								<th class="px-4 py-3 text-left font-medium">Resolves to</th>
								<th class="px-4 py-3 text-left font-medium">FQDN</th>
								<th class="px-4 py-3 text-left font-medium">Source</th>
								<th class="px-4 py-3 text-left font-medium"></th>
							</tr>
						</thead>
						<tbody>
							{#each allRecords as record}
								<tr class="border-b last:border-0 hover:bg-accent/50">
									<td class="px-4 py-3 font-mono font-medium">{record.name}</td>
									<td class="px-4 py-3 font-mono text-muted-foreground">{record.ip}</td>
									<td class="px-4 py-3 font-mono text-xs text-muted-foreground">{record.name}.{network.dnsDomain}</td>
									<td class="px-4 py-3">
										{#if record.source === 'auto'}
											<span class="rounded-full bg-muted px-2 py-0.5 text-xs text-muted-foreground">auto</span>
										{:else}
											<span class="rounded-full bg-primary/10 px-2 py-0.5 text-xs text-primary">custom</span>
										{/if}
									</td>
									<td class="px-4 py-3 text-right">
										{#if record.source === 'custom'}
											<button
												onclick={() => deleteDNS(record.id)}
												class="rounded px-2 py-1 text-xs text-destructive hover:bg-destructive/10"
											>
												Delete
											</button>
										{/if}
									</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			{/if}

		<!-- Join Tab -->
		{:else if activeTab === 'join'}
			<div class="max-w-xl space-y-8">
				<div>
					<h3 class="mb-1 text-lg font-medium">Join this network</h3>
					<p class="mb-4 text-sm text-muted-foreground">
						Connect your device to <span class="font-mono font-medium">{network.name}</span> and
						access services by name (e.g. <span class="font-mono">jellyfin.{network.dnsDomain}</span>).
					</p>

					<div class="space-y-3">
						<div class="rounded-lg border p-4">
							<p class="mb-2 text-sm font-medium">1. Install hop-agent</p>
							<p class="mb-2 text-xs text-muted-foreground">Auto-detects your OS and architecture.</p>
							<div class="rounded-md bg-muted p-3">
								<pre class="font-mono text-xs">{installScriptCmd}</pre>
							</div>
						</div>

						<div class="rounded-lg border p-4">
							<p class="mb-2 text-sm font-medium">2. Join as a client</p>
							<div class="rounded-md bg-muted p-3">
								<pre class="font-mono text-xs">sudo hop-agent enroll --client --endpoint {window.location.origin}</pre>
							</div>
							<p class="mt-2 text-xs text-muted-foreground">
								You'll be prompted to authorize in the browser. After joining, services are reachable as <span class="font-mono">hostname.{network.dnsDomain}</span>
							</p>
						</div>
					</div>
				</div>

				{#if isAdmin}
					<div class="border-t pt-6">
						<h3 class="mb-1 font-medium text-muted-foreground">Add a server instead?</h3>
						<p class="mb-3 text-sm text-muted-foreground">
							Install the agent on a server to manage it from the dashboard and make
							its services available on the mesh.
						</p>
						<button
							onclick={() => { showAddNode = true; addNode(); }}
							class="rounded-md border px-4 py-2 text-sm font-medium hover:bg-accent"
						>
							Add Server
						</button>
					</div>
				{/if}
			</div>

		<!-- Members Tab -->
		{:else if activeTab === 'members'}
			<div class="space-y-6">
				{#if isAdmin}
					<div class="flex items-center justify-between">
						<p class="text-sm text-muted-foreground">
							{networkMembers.length} {networkMembers.length === 1 ? 'member' : 'members'}
						</p>
						<button
							onclick={() => { showCreateInvite = true; inviteResult = null; }}
							class="rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground hover:bg-primary/90"
						>
							Create Invite
						</button>
					</div>
				{/if}

				{#if networkMembers.length > 0}
					<div class="rounded-lg border">
						<table class="w-full text-sm">
							<thead>
								<tr class="border-b bg-muted/50">
									<th class="px-4 py-3 text-left font-medium">Name</th>
									<th class="px-4 py-3 text-left font-medium">Email</th>
									<th class="px-4 py-3 text-left font-medium">Role</th>
									<th class="px-4 py-3 text-left font-medium">Joined</th>
									{#if isAdmin}<th class="px-4 py-3 text-left font-medium"></th>{/if}
								</tr>
							</thead>
							<tbody>
								{#each networkMembers as member}
									<tr class="border-b last:border-0 hover:bg-accent/50">
										<td class="px-4 py-3 font-medium">{member.name}</td>
										<td class="px-4 py-3 text-muted-foreground">{member.email}</td>
										<td class="px-4 py-3">
											<span class="rounded-full px-2 py-0.5 text-xs {member.role === 'admin' ? 'bg-primary/10 text-primary' : 'bg-muted text-muted-foreground'}">{member.role === 'admin' ? 'Admin' : 'Member'}</span>
										</td>
										<td class="px-4 py-3 text-muted-foreground">{timeAgo(member.createdAt)}</td>
										{#if isAdmin}
											<td class="px-4 py-3 text-right">
												{#if member.role !== 'admin'}
													<button
														onclick={() => removeMember(member.id)}
														class="rounded px-2 py-1 text-xs text-destructive hover:bg-destructive/10"
													>
														Remove
													</button>
												{/if}
											</td>
										{/if}
									</tr>
								{/each}
							</tbody>
						</table>
					</div>
				{:else}
					<div class="rounded-lg border border-dashed p-6 text-center">
						<p class="text-sm text-muted-foreground">No members yet. Create an invite to share this network.</p>
					</div>
				{/if}

				{#if isAdmin && networkInvites.length > 0}
					<div>
						<h3 class="mb-2 text-sm font-medium text-muted-foreground">Active Invites</h3>
						<div class="rounded-lg border">
							{#each networkInvites as invite}
								<div class="flex items-center justify-between border-b px-4 py-3 last:border-0">
									<div class="flex items-center gap-3">
										<span class="font-mono text-xs text-muted-foreground">{invite.code.slice(0, 12)}...</span>
										{#if invite.maxUses}
											<span class="text-xs text-muted-foreground">{invite.useCount}/{invite.maxUses} uses</span>
										{:else}
											<span class="text-xs text-muted-foreground">{invite.useCount} uses</span>
										{/if}
										{#if invite.expiresAt}
											{@const remaining = invite.expiresAt - Math.floor(Date.now() / 1000)}
											{#if remaining > 0}
												<span class="text-xs text-muted-foreground">expires {remaining > 86400 ? `in ${Math.floor(remaining / 86400)}d` : remaining > 3600 ? `in ${Math.floor(remaining / 3600)}h` : `in ${Math.floor(remaining / 60)}m`}</span>
											{:else}
												<span class="text-xs text-destructive">expired</span>
											{/if}
										{:else}
											<span class="text-xs text-muted-foreground">no expiry</span>
										{/if}
									</div>
									<div class="flex gap-1">
										<button onclick={() => copyInviteLink(invite.code)} class="rounded px-2 py-1 text-xs text-muted-foreground hover:text-foreground">Copy Link</button>
										<button onclick={() => revokeInvite(invite.id)} class="rounded px-2 py-1 text-xs text-destructive hover:bg-destructive/10">Revoke</button>
									</div>
								</div>
							{/each}
						</div>
					</div>
				{/if}
			</div>
		{/if}

		<!-- Create Invite Dialog -->
		{#if showCreateInvite}
			<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
				<div class="w-full max-w-sm rounded-lg border bg-card p-6 shadow-lg">
					{#if inviteResult}
						<h2 class="mb-4 text-lg font-semibold">Invite Created</h2>
						<p class="mb-2 text-sm text-muted-foreground">Share this link:</p>
						<div class="relative rounded-md bg-muted p-3 pr-16">
							<pre class="overflow-x-auto font-mono text-xs">{window.location.origin}/invite/{inviteResult.code}</pre>
							<button onclick={() => copyInviteLink(inviteResult!.code)} class="absolute right-2 top-2 rounded px-2 py-1 text-xs hover:bg-accent">
								Copy
							</button>
						</div>
						<div class="mt-4 flex justify-end">
							<button onclick={() => { showCreateInvite = false; }} class="rounded-md px-4 py-2 text-sm hover:bg-accent">Done</button>
						</div>
					{:else}
						<h2 class="mb-4 text-lg font-semibold">Create Invite</h2>
						<form onsubmit={createInvite} class="space-y-4">
							<div class="space-y-2">
								<label for="invite-expiry" class="text-sm font-medium">Expires in</label>
								<select id="invite-expiry" bind:value={inviteExpiresIn} class="w-full rounded-md border bg-background px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring">
									<option value="3600">1 hour</option>
									<option value="86400">24 hours</option>
									<option value="604800">7 days</option>
									<option value="2592000">30 days</option>
									<option value="0">Never</option>
								</select>
							</div>
							<div class="space-y-2">
								<label for="invite-max-uses" class="text-sm font-medium">Max uses</label>
								<input id="invite-max-uses" type="number" bind:value={inviteMaxUses} min="1" placeholder="Unlimited" class="w-full rounded-md border bg-background px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring" />
							</div>
							<div class="flex justify-end gap-2">
								<button type="button" onclick={() => { showCreateInvite = false; }} class="rounded-md px-4 py-2 text-sm hover:bg-accent">Cancel</button>
								<button type="submit" disabled={creatingInvite} class="rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90 disabled:opacity-50">
									{creatingInvite ? 'Creating...' : 'Create Invite'}
								</button>
							</div>
						</form>
					{/if}
				</div>
			</div>
		{/if}

		<!-- Add Server Dialog -->
		{#if showAddNode}
			<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
				<div class="w-full max-w-lg rounded-lg border bg-card p-6 shadow-lg">
					<h2 class="mb-4 text-lg font-semibold">Add Server</h2>
					{#if addingNode}
						<div class="flex items-center gap-3 py-4">
							<div class="h-5 w-5 animate-spin rounded-full border-2 border-primary border-t-transparent"></div>
							<span class="text-sm text-muted-foreground">Generating enrollment token...</span>
						</div>
					{:else if addNodeError}
						<div class="mb-4 rounded-md bg-destructive/10 p-3 text-sm text-destructive">{addNodeError}</div>
					{:else if nodeResult}
						<div class="space-y-4">
							<p class="text-sm text-muted-foreground">Run these commands on your server:</p>

							<div class="rounded-lg border p-4">
								<p class="mb-2 text-sm font-medium">1. Install hop-agent</p>
								<div class="relative rounded-md bg-muted p-3 pr-16">
									<pre class="font-mono text-xs">{installScriptCmd}</pre>
									<button
										onclick={() => copyToClipboard(installScriptCmd)}
										class="absolute right-2 top-2 rounded px-2 py-1 text-xs hover:bg-accent"
									>
										Copy
									</button>
								</div>
							</div>

							<div class="rounded-lg border p-4">
								<p class="mb-2 text-sm font-medium">2. Enroll</p>
								<div class="relative rounded-md bg-muted p-3 pr-16">
									<pre class="overflow-x-auto whitespace-pre-wrap break-all font-mono text-xs leading-relaxed">{enrollCommand}</pre>
									<button
										onclick={() => copyToClipboard(enrollCommand)}
										class="absolute right-2 top-2 rounded px-2 py-1 text-xs hover:bg-accent"
									>
										Copy
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

	<!-- Delete Network Dialog -->
	<Dialog open={showDeleteNetwork} onClose={() => { showDeleteNetwork = false; }}>
		<h2 class="mb-2 text-lg font-semibold">Delete "{network?.name}"?</h2>
		<p class="mb-4 text-sm text-muted-foreground">
			{#if network && visibleNodes.length > 0}
				This will permanently delete the network and disconnect {visibleNodes.length}
				{visibleNodes.length === 1 ? 'node' : 'nodes'}. Certificates and DNS records will stop working immediately.
			{:else}
				This will permanently delete the network. This cannot be undone.
			{/if}
		</p>
		{#if deleteNetworkError}
			<div class="mb-4 rounded-md bg-destructive/10 p-3 text-sm text-destructive">{deleteNetworkError}</div>
		{/if}
		<div class="mb-4 space-y-2">
			<label for="confirm-name" class="text-sm font-medium">
				Type "<span class="font-mono">{network?.name}</span>" to confirm
			</label>
			<input
				id="confirm-name"
				type="text"
				bind:value={deleteNetworkConfirm}
				class="w-full rounded-md border bg-background px-3 py-2 text-sm font-mono focus:outline-none focus:ring-2 focus:ring-ring"
			/>
		</div>
		<div class="flex justify-end gap-2">
			<button
				onclick={() => { showDeleteNetwork = false; }}
				disabled={deletingNetwork}
				class="rounded-md px-4 py-2 text-sm hover:bg-accent disabled:opacity-50"
			>
				Cancel
			</button>
			<button
				onclick={deleteNetwork}
				disabled={deletingNetwork || deleteNetworkConfirm !== network?.name}
				class="rounded-md bg-destructive px-4 py-2 text-sm font-medium text-destructive-foreground hover:bg-destructive/90 disabled:opacity-50"
			>
				{deletingNetwork ? 'Deleting...' : 'Delete Network'}
			</button>
		</div>
	</Dialog>

	<!-- Delete Node Dialog -->
	<Dialog open={showDeleteNode} onClose={() => { showDeleteNode = false; nodeToDelete = null; }}>
		<h2 class="mb-2 text-lg font-semibold">
			Delete node "{nodeToDelete?.hostname || nodeToDelete?.id?.slice(0, 8)}"?
		</h2>
		<p class="mb-4 text-sm text-muted-foreground">
			This will remove the node from the network and revoke its certificate.
			{#if nodeToDelete?.status === 'online'}
				<span class="font-medium text-destructive">This node is currently online.</span>
			{/if}
		</p>
		{#if deleteNodeError}
			<div class="mb-4 rounded-md bg-destructive/10 p-3 text-sm text-destructive">{deleteNodeError}</div>
		{/if}
		<div class="flex justify-end gap-2">
			<button
				onclick={() => { showDeleteNode = false; nodeToDelete = null; }}
				disabled={deletingNode}
				class="rounded-md px-4 py-2 text-sm hover:bg-accent disabled:opacity-50"
			>
				Cancel
			</button>
			<button
				onclick={deleteNode}
				disabled={deletingNode}
				class="rounded-md bg-destructive px-4 py-2 text-sm font-medium text-destructive-foreground hover:bg-destructive/90 disabled:opacity-50"
			>
				{deletingNode ? 'Deleting...' : 'Delete Node'}
			</button>
		</div>
	</Dialog>
</div>
