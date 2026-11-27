<script lang="ts">
	// "Who can reach this device": what hopssh keeps, the network's preset and
	// the rules of the network and of its rule sets that reach it, with where
	// each comes from (docs/wiki/concepts/firewall-rules.md, phase 3).
	import { onMount } from 'svelte';
	import { firewall as fwApi, ApiError } from '$lib/api/client';
	import type { NodeFirewallResponse } from '$lib/types/api';
	import { reachRows, reachNote } from '$lib/firewall-reach';
	import { Skeleton } from '$lib/components/ui/skeleton/index.js';

	let {
		networkId,
		nodeId,
		nodeName
	}: { networkId: string; nodeId: string; nodeName: (id: string) => string } = $props();

	let reach = $state<NodeFirewallResponse | null>(null);
	let failed = $state('');

	onMount(async () => {
		try {
			reach = await fwApi.node(networkId, nodeId);
		} catch (err) {
			failed = err instanceof ApiError ? err.message : 'Could not load what may reach this device';
		}
	});

	const rows = $derived(reach ? reachRows(reach, nodeName) : []);
	const note = $derived(reach ? reachNote(reach) : '');
</script>

<div class="mb-2 text-xs font-medium uppercase tracking-wider text-muted-foreground">Who can reach this device</div>
{#if failed}
	<p class="text-xs text-destructive">{failed}</p>
{:else if !reach}
	<Skeleton class="h-16 w-full" />
{:else}
	{#if note}
		<p class="mb-2 text-xs text-muted-foreground">{note}</p>
	{/if}
	<div class="overflow-x-auto rounded border">
		<table class="w-full text-xs">
			<thead>
				<tr class="border-b bg-background/50 text-left text-muted-foreground">
					<th class="px-3 py-2 font-medium">Protocol</th>
					<th class="px-3 py-2 font-medium">Port</th>
					<th class="px-3 py-2 font-medium">From</th>
					<th class="px-3 py-2 font-medium">Why</th>
				</tr>
			</thead>
			<tbody>
				{#each rows as r, i (i)}
					<tr
						class="border-b last:border-0 {r.applies ? '' : 'text-muted-foreground/60 line-through'}"
						title={r.applies ? '' : 'Kept out while the device blocks incoming'}
					>
						<td class="px-3 py-2">{r.proto}</td>
						<td class="px-3 py-2 font-mono">{r.port}</td>
						<td class="px-3 py-2">{r.from}</td>
						<td class="px-3 py-2 text-muted-foreground">{r.why}</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{/if}
