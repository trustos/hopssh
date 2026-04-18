<script lang="ts">
	import { onMount } from 'svelte';
	import {
		createTable,
		getCoreRowModel,
		getFilteredRowModel,
		getSortedRowModel,
		type ColumnDef,
		type SortingState,
		type ColumnFiltersState,
	} from '@tanstack/svelte-table';
	import * as Table from '$lib/components/ui/table/index.js';
	import { Badge } from '$lib/components/ui/badge/index.js';
	import { Button } from '$lib/components/ui/button/index.js';
	import { Input } from '$lib/components/ui/input/index.js';
	import { networkEvents as networkEventsApi } from '$lib/api/client';
	import type { NetworkEventResponse, NetworkDetailResponse } from '$lib/types/api';

	interface Props {
		networkId: string;
		/** Full network payload so we can resolve nodeId → hostname. */
		network: NetworkDetailResponse | null;
		/** Live ring buffer (from the WebSocket) — prepended + deduped. */
		liveEvents: LiveEvent[];
		/** Reactive unix-seconds ticker for timeAgo freshness. */
		now: number;
	}

	/**
	 * Shape of the in-memory ring-buffer entries pushed by the page's
	 * WebSocket handler. We accept these AND the persisted rows from the
	 * server and merge-dedupe them so the table always shows the most
	 * recent state.
	 */
	export interface LiveEvent {
		at: number;
		type: string;
		data: Record<string, unknown>;
	}

	let { networkId, network, liveEvents, now }: Props = $props();

	// Unified row shape — merged from history + live buffer.
	interface Row {
		id: string; // dedupe key: persisted "p-<id>" or live "l-<at>-<type>-<nodeId>"
		at: number;
		type: string;
		targetId?: string;
		targetHostname?: string;
		status?: string;
		detailsRaw?: string;
		data?: Record<string, unknown>;
		fromServer: boolean;
	}

	// Time-range buttons → unix-seconds "since" cutoff.
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

	// Per-fetch bookkeeping.
	let history = $state<NetworkEventResponse[]>([]);
	let loading = $state(false);
	let loadError = $state('');
	const PAGE_LIMIT = 200;

	async function loadHistory() {
		loading = true;
		loadError = '';
		try {
			history = await networkEventsApi.history(networkId, {
				since: rangeSince(range),
				limit: PAGE_LIMIT,
			});
		} catch (e) {
			loadError = e instanceof Error ? e.message : 'Failed to load activity history';
		} finally {
			loading = false;
		}
	}

	async function loadOlder() {
		if (history.length === 0) return;
		const oldest = history[history.length - 1].createdAt;
		loading = true;
		try {
			const older = await networkEventsApi.history(networkId, {
				since: rangeSince(range),
				limit: PAGE_LIMIT,
			});
			// Filter out anything we already have + anything newer than our
			// current oldest (the server returns newest-first from `since`,
			// so we merge then unique).
			const seen = new Set(history.map(h => h.id));
			for (const e of older) {
				if (e.createdAt < oldest && !seen.has(e.id)) {
					history = [...history, e];
					seen.add(e.id);
				}
			}
		} catch (e) {
			loadError = e instanceof Error ? e.message : 'Failed to load older';
		} finally {
			loading = false;
		}
	}

	onMount(() => { loadHistory(); });

	// Refetch whenever the user changes the range.
	$effect(() => {
		void range;
		loadHistory();
	});

	// Lookup hostname from the current network's node list so rows about
	// nodes that were deleted still show the last known hostname via the
	// server-side target_hostname LEFT JOIN.
	function resolveName(id: string | undefined, fallback?: string): string {
		if (!id) return '';
		const nodes = network?.nodes ?? [];
		const match = nodes.find(n => n.id === id);
		return match?.dnsName || match?.hostname || fallback || id.slice(0, 8);
	}

	// Merge live + history → deduped row array. Live wins for very
	// recent events the server might not have flushed yet.
	const rows = $derived<Row[]>((() => {
		const out: Row[] = [];
		const seen = new Set<string>();

		// Live events first (freshest).
		for (const e of liveEvents) {
			const nodeId = (e.data?.nodeId as string | undefined) ?? '';
			const key = `l-${e.at}-${e.type}-${nodeId}`;
			if (seen.has(key)) continue;
			seen.add(key);
			out.push({
				id: key,
				at: e.at,
				type: e.type,
				targetId: nodeId || undefined,
				targetHostname: resolveName(nodeId, undefined),
				status: (e.data?.status as string | undefined),
				data: e.data,
				fromServer: false,
			});
		}

		// Server history — skip entries that overlap with live by the
		// (at, type, nodeId) tuple.
		for (const e of history) {
			const liveKey = `l-${e.createdAt}-${e.type}-${e.targetId ?? ''}`;
			if (seen.has(liveKey)) continue;
			const key = `p-${e.id}`;
			if (seen.has(key)) continue;
			seen.add(key);
			out.push({
				id: key,
				at: e.createdAt,
				type: e.type,
				targetId: e.targetId,
				targetHostname: e.targetHostname ?? resolveName(e.targetId, undefined),
				status: e.status,
				detailsRaw: e.details,
				fromServer: true,
			});
		}

		return out.sort((a, b) => b.at - a.at);
	})());

	// Human-readable label for each event.
	function labelFor(r: Row): string {
		const who = r.targetHostname || resolveName(r.targetId, undefined) || '';
		let parsed: Record<string, unknown> | null = null;
		if (r.detailsRaw) {
			try { parsed = JSON.parse(r.detailsRaw); } catch { /* keep null */ }
		}
		const d = (r.data ?? parsed ?? {}) as Record<string, unknown>;

		switch (r.type) {
			case 'node.enrolled': {
				const host = (d.hostname as string | undefined) || who;
				return `${host} enrolled`;
			}
			case 'node.status':
				return `${who} ${r.status ?? 'updated'}`;
			case 'node.renamed':
				return `${who} renamed${d.name ? ` to "${d.name}"` : ''}`;
			case 'node.capabilities':
				return `${who} capabilities: ${Array.isArray(d.capabilities) ? (d.capabilities as string[]).join(', ') : 'updated'}`;
			case 'node.deleted':
				return `${who} deleted`;
			case 'dns.changed':    return 'DNS records changed';
			case 'member.changed': return 'membership changed';
			default:               return r.type;
		}
	}

	// Distinct event types available for filter pills. Derived from
	// rows so we don't hard-code a list that drifts from the backend.
	const eventTypes = $derived<string[]>((() => {
		const s = new Set<string>();
		for (const r of rows) s.add(r.type);
		return Array.from(s).sort();
	})());

	// TanStack table state. Sort by `at` desc by default, global search
	// + per-column event-type filter.
	let sorting = $state<SortingState>([{ id: 'at', desc: true }]);
	let columnFilters = $state<ColumnFiltersState>([]);
	let globalFilter = $state<string>('');

	function setEventTypeFilter(t: string | null) {
		if (t === null) {
			columnFilters = columnFilters.filter(f => f.id !== 'type');
			return;
		}
		const next = columnFilters.filter(f => f.id !== 'type');
		next.push({ id: 'type', value: t });
		columnFilters = next;
	}

	const activeTypeFilter = $derived<string | null>((() => {
		const f = columnFilters.find(x => x.id === 'type');
		return f ? String(f.value) : null;
	})());

	const columns: ColumnDef<Row>[] = [
		{
			id: 'at',
			accessorFn: r => r.at,
			header: 'When',
			cell: info => String(info.getValue()),
			sortingFn: 'basic',
		},
		{
			id: 'type',
			accessorFn: r => r.type,
			header: 'Event',
			filterFn: 'equals',
		},
		{
			id: 'target',
			accessorFn: r => r.targetHostname ?? '',
			header: 'Target',
		},
		{
			id: 'detail',
			accessorFn: r => labelFor(r),
			header: 'Detail',
		},
	];

	// Global filter: match against hostname + label + raw details.
	function globalFilterFn(row: { original: Row }, _colId: string, filter: string): boolean {
		if (!filter) return true;
		const q = filter.toLowerCase();
		const hay = [
			row.original.targetHostname ?? '',
			row.original.type,
			labelFor(row.original),
			row.original.detailsRaw ?? '',
			row.original.status ?? '',
		].join(' ').toLowerCase();
		return hay.includes(q);
	}

	const table = createTable<Row>({
		get data() { return rows; },
		columns,
		state: {
			get sorting() { return sorting; },
			get columnFilters() { return columnFilters; },
			get globalFilter() { return globalFilter; },
		},
		onSortingChange: u => { sorting = typeof u === 'function' ? u(sorting) : u; },
		onColumnFiltersChange: u => { columnFilters = typeof u === 'function' ? u(columnFilters) : u; },
		onGlobalFilterChange: u => { globalFilter = typeof u === 'function' ? u(globalFilter) : u; },
		globalFilterFn,
		getCoreRowModel: getCoreRowModel(),
		getSortedRowModel: getSortedRowModel(),
		getFilteredRowModel: getFilteredRowModel(),
	});

	// Format unix seconds as "2m ago" / "3h ago" etc. Re-computes on
	// every tick because `now` flows in.
	function timeAgo(ts: number): string {
		const diff = now - ts;
		if (diff < 5) return 'just now';
		if (diff < 60) return `${diff}s ago`;
		if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
		if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
		return `${Math.floor(diff / 86400)}d ago`;
	}

	function badgeVariantFor(type: string): 'default' | 'secondary' | 'destructive' | 'outline' {
		switch (type) {
			case 'node.deleted':      return 'destructive';
			case 'node.enrolled':     return 'default';
			case 'node.status':       return 'secondary';
			case 'node.renamed':      return 'outline';
			case 'node.capabilities': return 'outline';
			default:                  return 'secondary';
		}
	}

	// Headers we want to let users click to toggle sort.
	function toggleSort(colId: string) {
		const col = table.getColumn(colId);
		if (!col) return;
		const current = col.getIsSorted();
		if (!current)       col.toggleSorting(true);  // desc first
		else if (current === 'desc') col.toggleSorting(false); // asc
		else                 col.clearSorting();
	}
	function sortIndicator(colId: string): string {
		const col = table.getColumn(colId);
		if (!col) return '';
		const s = col.getIsSorted();
		if (s === 'desc') return '↓';
		if (s === 'asc')  return '↑';
		return '';
	}

	// Filtered + sorted row set from TanStack.
	const displayRows = $derived(table.getRowModel().rows);
</script>

<div class="space-y-3">
	<!-- Filter controls -->
	<div class="flex flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
		<div class="flex flex-wrap gap-1.5">
			<Button
				size="sm"
				variant={activeTypeFilter === null ? 'default' : 'outline'}
				onclick={() => setEventTypeFilter(null)}
			>All</Button>
			{#each eventTypes as t (t)}
				<Button
					size="sm"
					variant={activeTypeFilter === t ? 'default' : 'outline'}
					onclick={() => setEventTypeFilter(t)}
				>{t}</Button>
			{/each}
		</div>
		<div class="flex flex-wrap items-center gap-2">
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
				bind:value={globalFilter}
			/>
		</div>
	</div>

	{#if loadError}
		<div class="rounded-md border border-destructive/30 bg-destructive/10 p-2 text-xs text-destructive">
			{loadError}
		</div>
	{/if}

	{#if displayRows.length === 0 && !loading}
		<div class="rounded-lg border border-dashed p-8 text-center">
			<p class="mb-2 text-lg font-medium">No activity</p>
			<p class="text-sm text-muted-foreground">
				{history.length === 0 && liveEvents.length === 0
					? 'Events (enrollments, status changes, renames, capability changes) will appear here as they happen.'
					: 'No events match the current filters. Try widening the time range or clearing the search.'}
			</p>
		</div>
	{:else}
		<div class="rounded-lg border overflow-hidden">
			<Table.Root>
				<Table.Header>
					<Table.Row>
						<Table.Head
							class="cursor-pointer select-none"
							onclick={() => toggleSort('at')}
						>When {sortIndicator('at')}</Table.Head>
						<Table.Head
							class="cursor-pointer select-none"
							onclick={() => toggleSort('type')}
						>Event {sortIndicator('type')}</Table.Head>
						<Table.Head class="hidden sm:table-cell">Target</Table.Head>
						<Table.Head class="hidden md:table-cell">Detail</Table.Head>
					</Table.Row>
				</Table.Header>
				<Table.Body>
					{#each displayRows as row (row.original.id)}
						{@const r = row.original}
						<Table.Row>
							<Table.Cell class="text-xs text-muted-foreground whitespace-nowrap">
								<span title={new Date(r.at * 1000).toISOString()}>{timeAgo(r.at)}</span>
								{#if !r.fromServer}
									<span
										class="ml-1 text-[10px] uppercase tracking-wider text-muted-foreground/60"
										title="Live — not yet persisted"
									>live</span>
								{/if}
							</Table.Cell>
							<Table.Cell>
								<Badge variant={badgeVariantFor(r.type)}>{r.type}</Badge>
							</Table.Cell>
							<Table.Cell class="hidden sm:table-cell text-xs">
								{r.targetHostname ?? '—'}
							</Table.Cell>
							<Table.Cell class="hidden md:table-cell text-xs text-muted-foreground">
								{labelFor(r)}
							</Table.Cell>
						</Table.Row>
					{/each}
				</Table.Body>
			</Table.Root>
		</div>

		<div class="flex items-center justify-between text-xs text-muted-foreground">
			<span>
				Showing {displayRows.length} event{displayRows.length === 1 ? '' : 's'}
				{#if displayRows.length < rows.length}
					(of {rows.length} loaded)
				{/if}
			</span>
			{#if history.length >= PAGE_LIMIT && range !== 'all'}
				<Button size="sm" variant="outline" onclick={loadOlder} disabled={loading}>
					Load older
				</Button>
			{/if}
		</div>
	{/if}
</div>
