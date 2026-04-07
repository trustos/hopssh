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
	let nodeResultCreatedAt = $state(0);
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
	let inviteRole = $state<string>('member');
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
		// Tick every second for timeAgo + token countdown.
		const tickInterval = setInterval(() => {
			now = Math.floor(Date.now() / 1000);
		}, 1_000);

		// Debounced reload: prevents flickering from rapid WebSocket events.
		let reloadTimer: ReturnType<typeof setTimeout> | null = null;
		function debouncedReload() {
			if (reloadTimer) return; // already scheduled
			reloadTimer = setTimeout(() => {
				reloadTimer = null;
				loadNetwork();
			}, 500);
		}

		// WebSocket for real-time events with polling fallback.
		let ws: WebSocket | null = null;
		let wsRetryTimer: ReturnType<typeof setTimeout> | null = null;

		function connectEvents() {
			if (!networkId) return;
			const proto = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
			ws = new WebSocket(`${proto}//${window.location.host}/api/networks/${networkId}/events`);
			ws.onmessage = () => {
				debouncedReload();
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

		// Fallback poll: 30s normally. Faster (10s) when pending nodes exist.
		let lastPoll = 0;
		const pollInterval = setInterval(() => {
			const now = Date.now();
			const interval = hasPendingNodes ? 10_000 : 30_000;
			if (now - lastPoll < interval) return;
			if (!ws || ws.readyState !== WebSocket.OPEN || hasPendingNodes) {
				lastPoll = now;
				loadNetwork();
			}
		}, 5_000);

		return () => {
			clearInterval(tickInterval);
			clearInterval(pollInterval);
			if (reloadTimer) clearTimeout(reloadTimer);
			if (wsRetryTimer) clearTimeout(wsRetryTimer);
			if (ws) ws.close();
		};
	});

	async function loadNetwork() {
		// Only show loading skeleton on first load — subsequent refreshes update silently.
		if (!network) loading = true;
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
			nodeResultCreatedAt = Math.floor(Date.now() / 1000);
		} catch (e) {
			addNodeError = e instanceof ApiError ? e.message : 'Failed to create node';
		} finally {
			addingNode = false;
		}
	}

	let lastCopied = $state('');

	function copyToClipboard(text: string, label = 'Copied to clipboard') {
		const doCopy = () => {
			lastCopied = text;
			toast.success(label);
			setTimeout(() => { if (lastCopied === text) lastCopied = ''; }, 2000);
		};
		if (navigator.clipboard?.writeText) {
			navigator.clipboard.writeText(text).then(doCopy).catch(() => fallbackCopy(text, label));
		} else {
			fallbackCopy(text, label);
		}
	}

	function fallbackCopy(text: string, label: string) {
		const textarea = document.createElement('textarea');
		textarea.value = text;
		textarea.style.position = 'fixed';
		textarea.style.opacity = '0';
		document.body.appendChild(textarea);
		textarea.select();
		try {
			document.execCommand('copy');
			lastCopied = text;
			toast.success(label);
			setTimeout(() => { if (lastCopied === text) lastCopied = ''; }, 2000);
		} catch {
			toast.error('Copy failed — please select and copy manually');
		}
		document.body.removeChild(textarea);
	}

	function copyBtnLabel(text: string): string {
		return lastCopied === text ? 'Copied!' : 'Copy';
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
			copyToClipboard(addr, `Forwarding ${addr} — copied to clipboard`);
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
		copyToClipboard(`localhost:${port}`);
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
			const opts: { maxUses?: number; expiresIn?: number; role?: string } = {};
			if (inviteExpiresIn && inviteExpiresIn !== '0') opts.expiresIn = parseInt(inviteExpiresIn);
			if (inviteMaxUses) opts.maxUses = parseInt(inviteMaxUses);
			if (inviteRole) opts.role = inviteRole;
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
		copyToClipboard(`${window.location.origin}/invite/${code}`, 'Invite link copied');
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
			case 'offline': return 'bg-gray-400 dark:bg-gray-600';
			case 'pending': return 'bg-yellow-500/50 animate-pulse';
			default: return 'bg-muted-foreground/30';
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

	function hasCap(node: NodeResponse, cap: string): boolean {
		return node.capabilities?.includes(cap) ?? false;
	}

	async function toggleCapability(node: NodeResponse, cap: string) {
		const caps = node.capabilities || [];
		const newCaps = caps.includes(cap) ? caps.filter(c => c !== cap) : [...caps, cap];
		try {
			await nodesApi.updateCapabilities(networkId, node.id, newCaps);
			await loadNetwork();
		} catch (e) {
			toast.error(e instanceof ApiError ? e.message : 'Failed to update capabilities');
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
		<div class="mb-4 flex gap-1 border-b" role="tablist">
			{#each [
				{ id: 'join', label: 'Join' },
				{ id: 'nodes', label: `Nodes (${visibleNodes.length})` },
				{ id: 'dns', label: 'DNS' },
				{ id: 'members', label: `Members (${networkMembers.length})` }
			] as tab}
				<button
					role="tab"
					aria-selected={activeTab === tab.id}
					class="px-4 py-2 text-sm font-medium focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 {activeTab === tab.id ? 'border-b-2 border-primary text-primary' : 'text-muted-foreground hover:text-foreground'}"
					onclick={() => (activeTab = tab.id as typeof activeTab)}
				>
					{tab.label}
				</button>
			{/each}
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
						Add a node to get started — go to the <strong>Join</strong> tab.
					</p>
				</div>
			{:else}
				<div class="overflow-x-auto rounded-lg border">
					<table class="w-full text-sm">
						<thead>
							<tr class="border-b bg-muted/50">
								<th class="px-4 py-3 text-left font-medium">Status</th>
								<th class="px-4 py-3 text-left font-medium">Name</th>
								<th class="px-4 py-3 text-left font-medium">Capabilities</th>
								<th class="px-4 py-3 text-left font-medium">IP</th>
								<th class="px-4 py-3 text-left font-medium">DNS</th>
								<th class="px-4 py-3 text-left font-medium">Last Seen</th>
								<th class="px-4 py-3 text-right font-medium">Actions</th>
							</tr>
						</thead>
						<tbody>
							{#each visibleNodes as node}
								<tr class="border-b last:border-0 hover:bg-accent/50">
									<td class="px-4 py-3">
										<div class="flex items-center gap-2" title={node.status === 'pending' ? 'Waiting for agent enrollment. Run the enroll command on your device.' : ''}>
											<div class="h-2.5 w-2.5 rounded-full transition-colors duration-500 {statusColor(node.status)}"></div>
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
												{#if hasCap(node, 'terminal') && node.status === 'online'}
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
														class="cursor-pointer rounded px-1 text-xs text-muted-foreground/40 hover:text-foreground transition-colors"
														title="Rename node"
														aria-label="Rename node"
													>
														&#9998;
													</button>
												{/if}
											</span>
										{/if}
									</td>
									<td class="px-4 py-3">
										<div class="flex gap-1">
											{#each ['terminal', 'health', 'forward'] as cap}
												{#if isAdmin}
													<button
														onclick={() => toggleCapability(node, cap)}
														class="cursor-pointer rounded-full border px-2 py-0.5 text-xs transition-all {hasCap(node, cap) ? 'border-primary/30 bg-primary/15 text-primary hover:bg-primary/25' : 'border-transparent bg-muted text-muted-foreground/40 line-through hover:border-muted-foreground/20 hover:text-muted-foreground/60'}"
														title="{hasCap(node, cap) ? 'Click to disable' : 'Click to enable'} {cap}"
														aria-label="{hasCap(node, cap) ? 'Disable' : 'Enable'} {cap}"
													>
														{cap === 'terminal' ? 'TTY' : cap === 'health' ? 'Health' : 'Fwd'}
													</button>
												{:else if hasCap(node, cap)}
													<span class="rounded-full bg-primary/15 px-2 py-0.5 text-xs text-primary">{cap === 'terminal' ? 'TTY' : cap === 'health' ? 'Health' : 'Fwd'}</span>
												{/if}
											{/each}
										</div>
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
									<td class="px-4 py-3 text-right">
										<div class="flex justify-end gap-1">
											{#if hasCap(node, 'health') && node.status === 'online'}
												<button
													onclick={() => checkHealth(node)}
													class="rounded px-2 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground"
												>
													Health
												</button>
											{/if}
											{#if hasCap(node, 'terminal') && node.status === 'online'}
												<a
													href="/terminal/{networkId}/{node.id}?h={encodeURIComponent(node.hostname || node.id.slice(0, 8))}"
													class="rounded px-2 py-1 text-xs font-medium text-primary hover:bg-primary/10"
												>
													Terminal
												</a>
											{/if}
											{#if hasCap(node, 'forward') && node.status === 'online'}
												<button
													onclick={() => { forwardNodeId = forwardNodeId === node.id ? null : node.id; fwdRemotePort = ''; fwdLocalPort = ''; }}
													class="rounded px-2 py-1 text-xs text-primary hover:bg-primary/10"
												>
													Forward
												</button>
											{/if}
											{#if isAdmin}
												<span class="mx-0.5 border-l border-muted"></span>
												<button
													onclick={() => confirmDeleteNode(node)}
													class="rounded px-2 py-1 text-xs text-destructive/60 hover:text-destructive hover:bg-destructive/10"
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
												<span class="text-sm text-muted-foreground">Forward from {node.dnsName || node.hostname || node.id.slice(0, 8)}:</span>
												<div class="flex items-center gap-1">
													<span class="text-xs text-muted-foreground" title="Port on the remote node">Remote port</span>
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
													<span class="text-xs text-muted-foreground" title="Local port on your machine (auto-assigned if empty)">Local port</span>
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
				<div class="rounded-lg border border-dashed p-8 text-center">
					<p class="mb-1 text-sm text-muted-foreground">No DNS records yet.</p>
					<p class="text-xs text-muted-foreground">Node hostnames are added automatically when agents enroll. You can also add custom records.</p>
				</div>
			{:else}
				<div class="overflow-x-auto rounded-lg border">
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
									<td class="px-4 py-3">
										<span class="font-mono text-xs text-muted-foreground">{record.name}.{network.dnsDomain}</span>
										<button onclick={() => copyToClipboard(`${record.name}.${network.dnsDomain}`)} class="ml-1 rounded px-1 py-0.5 text-xs text-muted-foreground/40 hover:text-foreground" title="Copy FQDN">&#128203;</button>
									</td>
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
							<div class="relative rounded-md bg-muted p-3 pr-16">
								<pre class="font-mono text-xs">{installScriptCmd}</pre>
								<button onclick={() => copyToClipboard(installScriptCmd)} class="absolute right-2 top-2 rounded bg-muted px-2 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground">{copyBtnLabel(installScriptCmd)}</button>
							</div>
						</div>

						<div class="rounded-lg border p-4">
							<p class="mb-2 text-sm font-medium">2. Join the network</p>
							<div class="relative rounded-md bg-muted p-3 pr-16">
								<pre class="font-mono text-xs">sudo hop-agent enroll --endpoint {window.location.origin}</pre>
								<button onclick={() => copyToClipboard(`sudo hop-agent enroll --endpoint ${window.location.origin}`)} class="absolute right-2 top-2 rounded bg-muted px-2 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground">{copyBtnLabel(`sudo hop-agent enroll --endpoint ${window.location.origin}`)}</button>
							</div>
							<p class="mt-2 text-xs text-muted-foreground">
								You'll be prompted to authorize in the browser. After joining, services are reachable as <span class="font-mono">hostname.{network.dnsDomain}</span>
							</p>
						</div>
					</div>
				</div>

				{#if isAdmin}
					<div class="border-t pt-6">
						<h3 class="mb-1 font-medium text-muted-foreground">Add with a token instead?</h3>
						<p class="mb-3 text-sm text-muted-foreground">
							Generate a one-time token to enroll a node without the interactive device flow.
						</p>
						<button
							onclick={() => { showAddNode = true; addNode(); }}
							class="rounded-md border px-4 py-2 text-sm font-medium hover:bg-accent"
						>
							Add Node
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
					<div class="rounded-lg border border-dashed p-8 text-center">
						<p class="text-sm text-muted-foreground">You're the only member. Create an invite link to share this network with your team.</p>
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
							<button onclick={() => copyInviteLink(inviteResult!.code)} class="absolute right-2 top-2 rounded bg-muted px-2 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground">{copyBtnLabel(`${window.location.origin}/invite/${inviteResult.code}`)}</button>
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
							<div class="space-y-2">
								<label for="invite-role" class="text-sm font-medium">Role</label>
								<select id="invite-role" bind:value={inviteRole} class="w-full rounded-md border bg-background px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring">
									<option value="member">Member — can view nodes, join network, use terminal</option>
									<option value="admin">Admin — full access, can manage nodes, DNS, invites</option>
								</select>
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

		<!-- Add Node Dialog -->
		{#if showAddNode}
			<div class="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
				<div class="w-full max-w-lg rounded-lg border bg-card p-6 shadow-lg">
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
							<p class="text-sm text-muted-foreground">Run these commands on the device you want to add:</p>

							<div class="rounded-lg border p-4">
								<p class="mb-2 text-sm font-medium">1. Install hop-agent</p>
								<div class="relative rounded-md bg-muted p-3 pr-16">
									<pre class="font-mono text-xs">{installScriptCmd}</pre>
									<button
										onclick={() => copyToClipboard(installScriptCmd)}
										class="absolute right-2 top-2 rounded bg-muted px-2 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground"
									>{copyBtnLabel(installScriptCmd)}</button>
								</div>
							</div>

							<div class="rounded-lg border p-4">
								<p class="mb-2 text-sm font-medium">2. Enroll</p>
								<div class="relative rounded-md bg-muted p-3 pr-16">
									<pre class="overflow-x-auto whitespace-pre-wrap break-all font-mono text-xs leading-relaxed">{enrollCommand}</pre>
									<button
										onclick={() => copyToClipboard(enrollCommand)}
										class="absolute right-2 top-2 rounded bg-muted px-2 py-1 text-xs text-muted-foreground hover:bg-accent hover:text-foreground"
									>{copyBtnLabel(enrollCommand)}</button>
								</div>
							</div>

							<div class="text-xs text-muted-foreground">
								{#if nodeResultCreatedAt > 0}
									{#if (now - nodeResultCreatedAt) < 480}
										<p>Token expires in {Math.floor((600 - (now - nodeResultCreatedAt)) / 60)}m {(600 - (now - nodeResultCreatedAt)) % 60}s. IP: <span class="font-mono">{nodeResult.nebulaIP}</span></p>
									{:else if (now - nodeResultCreatedAt) < 600}
										<p class="text-destructive font-medium">Token expires in {600 - (now - nodeResultCreatedAt)}s! IP: <span class="font-mono">{nodeResult.nebulaIP}</span></p>
									{:else}
										<p class="text-destructive font-medium">Token expired — close and generate a new one.</p>
									{/if}
								{/if}
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
