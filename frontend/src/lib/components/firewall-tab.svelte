<!--
	The Firewall tab of a network: its preset, its own rules and the rule sets
	its owner attached (docs/wiki/decisions/firewall-rule-sets.md). What a
	network allows is the union of all of them; rules only allow.

	A change is saved as a whole with the version it was made against; when
	someone changed it first (412) the tab reloads and the change is made again.
	Attaching, detaching and changing a rule set keep an unsaved change of the
	network's own rules.
-->
<script lang="ts">
	import { onMount } from 'svelte';
	import { toast } from 'svelte-sonner';
	import { firewall as fwApi, ApiError } from '$lib/api/client';
	import type {
		FirewallDevice,
		FirewallPreset,
		FirewallRule,
		FirewallSetResponse,
		NetworkFirewallResponse,
		NodeResponse
	} from '$lib/types/api';
	import * as Alert from '$lib/components/ui/alert/index.js';
	import { Button } from '$lib/components/ui/button/index.js';
	import { Input } from '$lib/components/ui/input/index.js';
	import { Label } from '$lib/components/ui/label/index.js';
	import { Badge } from '$lib/components/ui/badge/index.js';
	import { Skeleton } from '$lib/components/ui/skeleton/index.js';

	// nodes: every device of the network (not only the ones shown on the
	// Nodes tab), so that a rule naming an offline one doesn't read as deleted.
	let { networkId, nodes }: { networkId: string; nodes: NodeResponse[] } = $props();

	const presets: { value: FirewallPreset; label: string; hint: string }[] = [
		{ value: 'all', label: 'Everything', hint: 'Every protocol between devices, UDP included.' },
		{ value: 'tcp', label: 'TCP only', hint: 'Every TCP port between devices; no UDP.' },
		{
			value: 'ping',
			label: 'Only what hopssh needs',
			hint: 'Management and ping. Rules below add the rest.'
		}
	];
	const protos: FirewallRule['proto'][] = ['tcp', 'udp', 'icmp', 'any'];
	const selectClass =
		'rounded-md border bg-background px-2 py-1 text-sm focus:outline-none focus:ring-2 focus:ring-ring disabled:opacity-60';

	let policy = $state<NetworkFirewallResponse | null>(null);
	let loadError = $state('');
	let mySets = $state<FirewallSetResponse[]>([]);

	// The network's draft, saved as a whole.
	let preset = $state<FirewallPreset>('all');
	let rules = $state<FirewallRule[]>([]);
	let saving = $state(false);

	// The rule set being edited; id '' for a new one.
	let editing = $state<{ id: string; version: number; name: string; rules: FirewallRule[] } | null>(
		null
	);
	let savingSet = $state(false);
	let attachId = $state('');

	const nodeNames = $derived(
		new Map(nodes.map((n) => [n.id, n.dnsName || n.hostname || `device ${n.id.slice(0, 8)}`]))
	);
	const attachable = $derived(mySets.filter((s) => !policy?.sets.some((a) => a.id === s.id)));
	const dirty = $derived(
		policy !== null &&
			(preset !== policy.preset || JSON.stringify(clean(rules)) !== JSON.stringify(clean(policy.rules)))
	);

	// clean drops what the server reads as absent, so that a rule set to
	// "any device" compares equal to one that never named a device.
	function clean(list: FirewallRule[]): FirewallRule[] {
		return list.map((r) => {
			const out: FirewallRule = { proto: r.proto, port: r.port.trim() };
			if (r.sourceNodeId) out.sourceNodeId = r.sourceNodeId;
			if (r.targetNodeId) out.targetNodeId = r.targetNodeId;
			if (r.description?.trim()) out.description = r.description.trim();
			return out;
		});
	}

	function copy(list: FirewallRule[]): FirewallRule[] {
		return list.map((r) => ({ ...r }));
	}

	// unknown: what an ID that is not a device of this network is. In the
	// network's rules and in an attached set the device was deleted; a set
	// that is not attached here may name a device of another network.
	function nodeLabel(id: string | undefined, empty: string, unknown: string): string {
		if (!id) return empty;
		return nodeNames.get(id) ?? unknown;
	}

	const deleted = 'deleted device';
	function unknownInSet(setId: string): string {
		return policy?.sets.some((a) => a.id === setId) ? deleted : 'not a device of this network';
	}

	function message(err: unknown, fallback: string): string {
		return err instanceof ApiError ? err.message : fallback;
	}

	function apply(p: NetworkFirewallResponse) {
		policy = p;
		preset = p.preset;
		rules = copy(p.rules);
	}

	// load reads everything. With keepDraft, an unsaved change of the
	// network's rules stays, together with the version it was made against:
	// if someone changed them meanwhile, its save is refused (412).
	async function load(keepDraft = false) {
		loadError = '';
		try {
			const p = await fwApi.get(networkId);
			if (keepDraft && policy && dirty) {
				const { sets, canEdit, canAttach, managed, devices } = p;
				policy = { ...policy, sets, canEdit, canAttach, managed, devices };
			} else {
				apply(p);
			}
			mySets = p.canAttach ? await fwApi.listSets() : [];
		} catch (err) {
			loadError = message(err, 'Could not load the firewall rules');
		}
	}

	onMount(() => load());

	const stale = (err: unknown) => err instanceof ApiError && err.status === 412;

	async function save() {
		if (!policy) return;
		saving = true;
		try {
			apply(await fwApi.update(networkId, policy.version, preset, clean(rules)));
			toast.success('Firewall rules saved');
		} catch (err) {
			if (stale(err)) {
				toast.error('Someone changed these rules first. They are reloaded: make your change again.');
				await load();
			} else toast.error(message(err, 'Could not save the rules'));
		} finally {
			saving = false;
		}
	}

	function revert() {
		if (policy) apply(policy);
	}

	async function attach() {
		if (!attachId) return;
		try {
			await fwApi.attach(networkId, attachId);
			attachId = '';
			await load(true);
		} catch (err) {
			toast.error(message(err, 'Could not attach the rule set'));
		}
	}

	async function detach(set: FirewallSetResponse) {
		if (!confirm(`Detach "${set.name}"? Its rules stop applying to this network.`)) return;
		try {
			await fwApi.detach(networkId, set.id);
			await load(true);
		} catch (err) {
			toast.error(message(err, 'Could not detach the rule set'));
		}
	}

	function editSet(set: FirewallSetResponse | null) {
		const own = set ? mySets.find((s) => s.id === set.id) ?? set : null;
		editing = own
			? { id: own.id, version: own.version, name: own.name, rules: copy(own.rules) }
			: { id: '', version: 0, name: '', rules: [] };
	}

	async function saveSet() {
		if (!editing) return;
		savingSet = true;
		try {
			if (editing.id) {
				await fwApi.updateSet(editing.id, editing.version, editing.name, clean(editing.rules));
			} else {
				await fwApi.createSet(editing.name, clean(editing.rules));
			}
			toast.success(`Rule set "${editing.name.trim()}" saved`);
			editing = null;
			await load(true);
		} catch (err) {
			if (stale(err) && editing?.id) {
				// Reopen it as it is now; a 409 (a name already taken) keeps the editor.
				const id = editing.id;
				toast.error('Someone changed this rule set first. It is reloaded: make your change again.');
				await load(true);
				const fresh = mySets.find((s) => s.id === id);
				editing = fresh
					? { id: fresh.id, version: fresh.version, name: fresh.name, rules: copy(fresh.rules) }
					: null;
			} else toast.error(message(err, 'Could not save the rule set'));
		} finally {
			savingSet = false;
		}
	}

	async function deleteSet() {
		if (!editing?.id) return;
		if (!confirm(`Delete the rule set "${editing.name}"?`)) return;
		try {
			await fwApi.deleteSet(editing.id);
			editing = null;
			await load(true);
		} catch (err) {
			toast.error(message(err, 'Could not delete the rule set'));
		}
	}

	function attachedCount(id: string): number {
		return mySets.find((s) => s.id === id)?.networkIds?.length ?? 0;
	}

	// What a device runs, from its last report (docs/wiki/concepts/firewall-rules.md,
	// "Delivery and apply", point 8). A device that runs the rules it should
	// is applied, whatever it refused before. One whose own switch "Block
	// incoming" is on keeps these rules for when it is switched off.
	type DeviceStatus = { label: string; tone: 'ok' | 'wait' | 'bad'; detail: string };
	function deviceStatus(d: FirewallDevice): DeviceStatus {
		const runs = d.fingerprint ? 'an earlier version of the rules' : 'the rules it got when it joined';
		const blocked = (then: string): DeviceStatus => ({
			label: 'Blocks incoming',
			tone: 'wait',
			detail: `Switched on at the device: only what hopssh needs gets in. ${then}`
		});
		if (!d.reportedAt)
			return {
				label: 'No report',
				tone: 'wait',
				detail:
					'An agent of v0.11.31 or older sends none, and keeps the rules it got when it joined. A newer one reports at its next heartbeat.'
			};
		if (d.fingerprint === d.expected)
			return d.blocked
				? blocked('It has these rules, and runs them once it is switched off.')
				: { label: 'Applied', tone: 'ok', detail: '' };
		if (d.error)
			return {
				label: 'Refused',
				tone: 'bad',
				detail: `${d.error}. It runs ${runs}.${d.blocked ? ' It also blocks incoming.' : ''}`
			};
		if (d.blocked) return blocked('It takes these at its next heartbeat, and runs them once it is switched off.');
		return { label: 'Pending', tone: 'wait', detail: `It runs ${runs}, and takes these at its next heartbeat.` };
	}
</script>

<!-- One editable list of rules: the network's own, or a rule set's. -->
{#snippet ruleList(list: FirewallRule[], editable: boolean, unknown: string)}
	{#if list.length === 0}
		<p class="px-4 py-3 text-sm text-muted-foreground">No rules.</p>
	{:else}
		<div class="overflow-x-auto">
			<table class="w-full text-sm">
				<thead>
					<tr class="border-b text-left text-xs text-muted-foreground">
						<th class="px-4 py-2 font-medium">Protocol</th>
						<th class="px-4 py-2 font-medium">Port</th>
						<th class="px-4 py-2 font-medium">From</th>
						<th class="px-4 py-2 font-medium">To</th>
						<th class="px-4 py-2 font-medium">Description</th>
						<th></th>
					</tr>
				</thead>
				<tbody>
					{#each list as rule, i (i)}
						<tr class="border-b last:border-0">
							{#if editable}
								<td class="px-4 py-2">
									<select
										aria-label="Protocol"
										class={selectClass}
										bind:value={rule.proto}
										onchange={(e) => {
											if (e.currentTarget.value === 'icmp') rule.port = 'any';
										}}
									>
										{#each protos as p (p)}<option value={p}>{p}</option>{/each}
									</select>
								</td>
								<td class="px-4 py-2">
									<Input
										aria-label="Port"
										class="h-8 w-28 font-mono"
										placeholder="22, 8000-8100, any"
										disabled={rule.proto === 'icmp'}
										bind:value={rule.port}
									/>
								</td>
								{#each [['sourceNodeId', 'From', 'Any device'], ['targetNodeId', 'To', 'Every device']] as [field, what, empty] (field)}
									{@const key = field as 'sourceNodeId' | 'targetNodeId'}
									<td class="px-4 py-2">
										<select aria-label={what} class={selectClass} bind:value={rule[key]}>
											<option value={undefined}>{empty}</option>
											{#each nodes as n (n.id)}
												<option value={n.id}>{nodeNames.get(n.id)}</option>
											{/each}
											{#if rule[key] && !nodeNames.has(rule[key]!)}
												<option value={rule[key]}>{unknown}</option>
											{/if}
										</select>
									</td>
								{/each}
								<td class="px-4 py-2">
									<Input
										aria-label="Description"
										class="h-8 min-w-40"
										placeholder="What it is for"
										bind:value={rule.description}
									/>
								</td>
								<td class="px-4 py-2 text-right">
									<button
										class="rounded px-2 py-1 text-xs text-destructive hover:bg-destructive/10"
										onclick={() => list.splice(i, 1)}
									>
										Remove
									</button>
								</td>
							{:else}
								<td class="px-4 py-2 font-mono">{rule.proto}</td>
								<td class="px-4 py-2 font-mono">{rule.port}</td>
								<td class="px-4 py-2">{nodeLabel(rule.sourceNodeId, 'Any device', unknown)}</td>
								<td class="px-4 py-2">{nodeLabel(rule.targetNodeId, 'Every device', unknown)}</td>
								<td class="px-4 py-2 text-muted-foreground">{rule.description ?? ''}</td>
								<td></td>
							{/if}
						</tr>
					{/each}
				</tbody>
			</table>
		</div>
	{/if}
	{#if editable}
		<div class="border-t px-4 py-2">
			<button
				class="rounded px-2 py-1 text-xs font-medium text-primary hover:bg-primary/10"
				onclick={() => list.push({ proto: 'tcp', port: '' })}
			>
				+ Add rule
			</button>
		</div>
	{/if}
{/snippet}

{#if loadError}
	<Alert.Root variant="destructive">
		<Alert.Description>{loadError}</Alert.Description>
	</Alert.Root>
{:else if !policy}
	<Skeleton class="h-40 w-full" />
{:else}
	<div class="space-y-6">
		<Alert.Root>
			<Alert.Description>
				{#if policy.managed}
					Devices apply a change at their next heartbeat; a connection that a removed rule allowed
					stops. Agents of v0.11.31 and older don't apply them: they keep the rules they got when
					they joined.
				{:else}
					These rules are not sent to devices yet: each keeps the rules it got when it joined. The
					first save, or the first rule set attached, sends them to every device, and from then on
					each change.
				{/if}
				Whatever is chosen, hopssh keeps its own management connection and ping.
			</Alert.Description>
		</Alert.Root>

		<section class="space-y-2">
			<h3 class="text-sm font-medium">What devices allow from each other</h3>
			<div class="rounded-lg border p-4">
				<div class="space-y-2">
					<Label for="fw-preset">Preset</Label>
					<select
						id="fw-preset"
						class={selectClass + ' w-full sm:w-80'}
						bind:value={preset}
						disabled={!policy.canEdit}
					>
						{#each presets as p (p.value)}<option value={p.value}>{p.label}</option>{/each}
					</select>
					<p class="text-xs text-muted-foreground">
						{presets.find((p) => p.value === preset)?.hint}
					</p>
				</div>
			</div>
		</section>

		<section class="space-y-2">
			<h3 class="text-sm font-medium">Rules of this network</h3>
			<p class="text-xs text-muted-foreground">
				Each rule allows traffic in addition to the preset. There are no deny rules, so their
				order doesn't matter.
			</p>
			<div class="rounded-lg border">
				{@render ruleList(rules, policy.canEdit, deleted)}
			</div>
			{#if policy.canEdit}
				<div class="flex items-center gap-2">
					<Button size="sm" onclick={save} disabled={!dirty || saving}>
						{saving ? 'Saving…' : 'Save'}
					</Button>
					<Button size="sm" variant="outline" onclick={revert} disabled={!dirty || saving}>
						Undo changes
					</Button>
					<span class="text-xs text-muted-foreground">Version {policy.version}</span>
				</div>
			{:else}
				<p class="text-xs text-muted-foreground">Only the owner and admins change these rules.</p>
			{/if}
		</section>

		{#if policy.managed}
			<section class="space-y-2">
				<div class="flex items-center justify-between gap-2">
					<h3 class="text-sm font-medium">On the devices</h3>
					<Button size="xs" variant="outline" onclick={() => load(true)}>Refresh</Button>
				</div>
				<div class="overflow-x-auto rounded-lg border">
					<table class="w-full text-sm">
						<thead>
							<tr class="border-b text-left text-xs text-muted-foreground">
								<th class="px-4 py-2 font-medium">Device</th>
								<th class="px-4 py-2 font-medium">Rules</th>
								<th class="px-4 py-2 font-medium">Since</th>
								<th class="px-4 py-2 font-medium"></th>
							</tr>
						</thead>
						<tbody>
							{#each policy.devices as d (d.nodeId)}
								{@const s = deviceStatus(d)}
								<tr class="border-b last:border-0">
									<td class="px-4 py-2">{nodeLabel(d.nodeId, '', `device ${d.nodeId.slice(0, 8)}`)}</td>
									<td class="px-4 py-2">
										<Badge
											variant={s.tone === 'ok' ? 'default' : s.tone === 'bad' ? 'destructive' : 'outline'}
										>
											{s.label}
										</Badge>
									</td>
									<td class="px-4 py-2 whitespace-nowrap text-muted-foreground">
										{d.reportedAt ? new Date(d.reportedAt * 1000).toLocaleString() : ''}
									</td>
									<td class="px-4 py-2 text-xs text-muted-foreground">{s.detail}</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
			</section>
		{/if}

		<section class="space-y-2">
			<h3 class="text-sm font-medium">Shared rule sets</h3>
			<p class="text-xs text-muted-foreground">
				Rule sets belong to the network's owner, who can attach one set to several networks. Their
				rules apply here as well.
			</p>
			{#if policy.sets.length === 0}
				<div class="rounded-lg border border-dashed p-4 text-center text-sm text-muted-foreground">
					No rule set is attached.
				</div>
			{/if}
			{#each policy.sets as set (set.id)}
				<div class="rounded-lg border">
					<div class="flex items-center justify-between gap-2 border-b px-4 py-2">
						<div class="flex items-center gap-2">
							<span class="font-medium">{set.name}</span>
							<Badge variant="secondary">
								{set.rules.length}
								{set.rules.length === 1 ? 'rule' : 'rules'}
							</Badge>
						</div>
						{#if policy.canAttach}
							<div class="flex gap-1">
								<Button size="xs" variant="outline" onclick={() => editSet(set)}>Edit</Button>
								<Button size="xs" variant="destructive" onclick={() => detach(set)}>Detach</Button>
							</div>
						{/if}
					</div>
					{@render ruleList(set.rules, false, deleted)}
				</div>
			{/each}

			{#if policy.canAttach}
				<div class="flex flex-wrap items-center gap-2">
					{#if attachable.length > 0}
						<select aria-label="Rule set to attach" class={selectClass} bind:value={attachId}>
							<option value="">Choose a rule set…</option>
							{#each attachable as s (s.id)}<option value={s.id}>{s.name}</option>{/each}
						</select>
						<Button size="sm" variant="outline" onclick={attach} disabled={!attachId}>Attach</Button>
					{/if}
					{#each attachable as s (s.id)}
						<Button size="xs" variant="ghost" onclick={() => editSet(s)}>Edit "{s.name}"</Button>
					{/each}
					<Button size="sm" variant="outline" onclick={() => editSet(null)}>New rule set</Button>
				</div>
			{:else}
				<p class="text-xs text-muted-foreground">Only the network's owner attaches rule sets.</p>
			{/if}

			{#if editing}
				<div class="space-y-3 rounded-lg border p-4">
					<div class="space-y-2">
						<Label for="fw-set-name">{editing.id ? 'Rule set' : 'New rule set'}</Label>
						<Input id="fw-set-name" class="sm:w-80" placeholder="web servers" bind:value={editing.name} />
						{#if editing.id && attachedCount(editing.id) > 1}
							<p class="text-xs text-muted-foreground">
								Attached to {attachedCount(editing.id)} networks: a change applies to all of them.
							</p>
						{/if}
					</div>
					<div class="rounded-lg border">
						{@render ruleList(editing.rules, true, unknownInSet(editing.id))}
					</div>
					<div class="flex flex-wrap items-center gap-2">
						<Button size="sm" onclick={saveSet} disabled={savingSet || !editing.name.trim()}>
							{savingSet ? 'Saving…' : 'Save rule set'}
						</Button>
						<Button size="sm" variant="outline" onclick={() => (editing = null)}>Cancel</Button>
						{#if editing.id}
							<Button
								size="sm"
								variant="destructive"
								onclick={deleteSet}
								disabled={attachedCount(editing.id) > 0}
							>
								Delete
							</Button>
							{#if attachedCount(editing.id) > 0}
								<span class="text-xs text-muted-foreground">
									Detach it from every network to delete it.
								</span>
							{/if}
						{/if}
					</div>
				</div>
			{/if}
		</section>
	</div>
{/if}
