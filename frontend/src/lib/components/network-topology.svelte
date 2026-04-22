<script lang="ts">
	import { onMount, onDestroy } from 'svelte';
	import cytoscape from 'cytoscape';
	// @ts-expect-error — fcose has no TypeScript types bundled
	import fcose from 'cytoscape-fcose';
	import type { NodeResponse } from '$lib/types/api';
	import { displayStatus } from '$lib/node-status';

	interface Props {
		nodes: NodeResponse[];
		/** current unix seconds ticker; drives status freshness */
		now: number;
	}
	let { nodes, now }: Props = $props();

	cytoscape.use(fcose);

	let container: HTMLDivElement;
	let cy: cytoscape.Core | null = null;

	// Map server `connectivity` / `displayStatus` to node colour.
	// Lighthouse is its own shape + colour (diamond, gray).
	function nodeColour(n: NodeResponse, nowSec: number): string {
		if (n.nodeType === 'lighthouse') return '#64748b'; // slate-500
		const status = displayStatus(n, nowSec);
		// Degraded = heartbeat fine, no mesh peers (portmap/NAT broken).
		// Distinct orange distinguishes from offline-gray and online-green.
		if (status === 'degraded') return '#f97316'; // orange-500
		if (status !== 'online') return '#9ca3af'; // gray-400
		switch (n.connectivity) {
			case 'direct':  return '#10b981'; // emerald-500
			case 'relayed': return '#3b82f6'; // blue-500
			case 'mixed':   return '#f59e0b'; // amber-500
			default:        return '#6b7280'; // gray-500 (idle / unreported)
		}
	}

	// Build cytoscape elements from the network's node list + each
	// node's reported peers. Each directed edge carries the reporter's
	// view of one peer (A→B direct OR A→B relayed). If both A and B
	// report each other, we end up with two edges — one per direction.
	// That IS the diagnostic signal; asymmetric views (A says direct,
	// B says relayed) show as two different-colored edges.
	function buildElements(ns: NodeResponse[], nowSec: number) {
		const elements: cytoscape.ElementDefinition[] = [];
		const byVpn = new Map<string, NodeResponse>();
		for (const n of ns) {
			const vpn = (n.nebulaIP ?? '').split('/')[0];
			if (vpn) byVpn.set(vpn, n);
			elements.push({
				data: {
					id: n.id,
					label: n.dnsName || n.hostname || n.id.slice(0, 8),
					colour: nodeColour(n, nowSec),
					shape: n.nodeType === 'lighthouse' ? 'diamond' : 'ellipse',
				},
			});
		}
		for (const src of ns) {
			if (!src.peers || src.peers.length === 0) continue;
			for (const p of src.peers) {
				const dst = byVpn.get(p.vpnAddr);
				if (!dst) continue; // peer not in this network's nodes list (lighthouse or stale)
				elements.push({
					data: {
						id: `${src.id}→${dst.id}`,
						source: src.id,
						target: dst.id,
						colour: p.direct ? '#10b981' : '#3b82f6',
						kind: p.direct ? 'direct' : 'relayed',
					},
				});
			}
		}
		return elements;
	}

	onMount(() => {
		cy = cytoscape({
			container,
			elements: buildElements(nodes, now),
			style: [
				{
					selector: 'node',
					style: {
						'background-color': 'data(colour)',
						'label': 'data(label)',
						'shape': 'data(shape)',
						'color': '#e5e7eb',
						'font-size': 11,
						'font-family': "'JetBrains Mono', monospace",
						'text-valign': 'bottom',
						'text-margin-y': 6,
						'text-outline-color': '#0a0e14',
						'text-outline-width': 2,
						'width': 24,
						'height': 24,
						'border-width': 2,
						'border-color': '#0a0e14',
					},
				},
				{
					selector: 'edge',
					style: {
						'curve-style': 'bezier',
						'line-color': 'data(colour)',
						'target-arrow-color': 'data(colour)',
						'target-arrow-shape': 'triangle',
						'width': 2,
						'opacity': 0.7,
						'arrow-scale': 0.8,
					},
				},
				{
					selector: 'edge:selected',
					style: {
						'width': 4,
						'opacity': 1,
					},
				},
			],
			layout: {
				name: 'fcose',
				animate: false,
				randomize: true,
				quality: 'default',
				nodeRepulsion: 8000,
				idealEdgeLength: 120,
				// @ts-expect-error — fcose options not in core types
				packComponents: true,
			},
			wheelSensitivity: 0.2,
			minZoom: 0.3,
			maxZoom: 3,
		});
	});

	// React to prop changes without nuking the user's pan/zoom/drag.
	//
	// The 1 s `now` ticker only shifts colors (status / connectivity);
	// the node+edge SET rarely changes. So:
	//   * Update data.colour in place for elements already in the
	//     graph — no re-layout, no view reset.
	//   * Add new elements as they appear; remove elements that
	//     vanished.
	//   * Re-run fcose layout ONLY when the structure changed
	//     (nodes or edges added/removed). Drag-positioned nodes and
	//     the user's pan/zoom survive steady-state ticks.
	$effect(() => {
		if (!cy) return;
		const newEls = buildElements(nodes, now);
		const incoming = new Set(newEls.map(el => String(el.data.id)));
		let structureChanged = false;

		for (const el of newEls) {
			const existing = cy.getElementById(String(el.data.id));
			if (existing.empty()) {
				cy.add(el);
				structureChanged = true;
			} else {
				// In-place data update — preserves position.
				for (const [k, v] of Object.entries(el.data)) {
					if (k === 'id') continue;
					existing.data(k, v);
				}
			}
		}

		// Drop elements that left the graph.
		cy.elements().forEach(el => {
			if (!incoming.has(el.id())) {
				el.remove();
				structureChanged = true;
			}
		});

		if (structureChanged) {
			cy.layout({ name: 'fcose', animate: false, randomize: false } as any).run();
		}
	});

	// Recenter: fit everything into view with a small margin, and
	// center. Useful after the user has panned far off-screen or
	// zoomed in on one node.
	function recenter() {
		if (!cy) return;
		cy.fit(undefined, 30);
		cy.center();
	}

	onDestroy(() => {
		cy?.destroy();
		cy = null;
	});
</script>

<div class="relative">
	<div bind:this={container} class="h-[500px] w-full rounded-lg border bg-[#0a0e14]"></div>
	<!-- Recenter button -->
	<button
		onclick={recenter}
		class="absolute right-3 top-3 flex items-center gap-1.5 rounded-md border border-border/50 bg-background/90 backdrop-blur-sm px-2.5 py-1.5 text-[10px] font-medium text-muted-foreground hover:text-foreground hover:bg-background transition-colors"
		title="Reset pan + zoom to fit everything"
		aria-label="Recenter topology view"
	>
		<svg class="h-3 w-3" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><circle cx="12" cy="12" r="3"/></svg>
		Recenter
	</button>
	<!-- Legend -->
	<div class="absolute left-3 top-3 flex flex-col gap-1.5 rounded-md border border-border/50 bg-background/90 backdrop-blur-sm p-2.5 text-[10px]">
		<div class="mb-0.5 font-medium uppercase tracking-wider text-muted-foreground">Legend</div>
		<div class="flex items-center gap-2"><span class="inline-block h-2 w-6 rounded-full bg-emerald-500"></span> direct (P2P)</div>
		<div class="flex items-center gap-2"><span class="inline-block h-2 w-6 rounded-full bg-blue-500"></span> relayed</div>
		<div class="flex items-center gap-2"><span class="inline-block h-3 w-3 rounded-full bg-emerald-500"></span> online direct</div>
		<div class="flex items-center gap-2"><span class="inline-block h-3 w-3 rounded-full bg-amber-500"></span> online mixed</div>
		<div class="flex items-center gap-2"><span class="inline-block h-3 w-3 rounded-full bg-blue-500"></span> online relayed</div>
		<div class="flex items-center gap-2"><span class="inline-block h-3 w-3 rounded-full bg-gray-400"></span> offline</div>
		<div class="flex items-center gap-2"><span class="inline-block h-3 w-3 bg-slate-500" style="transform:rotate(45deg);"></span> lighthouse</div>
	</div>
</div>
