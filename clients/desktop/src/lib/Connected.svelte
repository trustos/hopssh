<script lang="ts">
  import { agent } from './stores.svelte';
  import { local, type PeerDetail, type EnrollmentStatus } from './local-api';
  import { openExternal } from './tauri-bridge';
  import SystemModeCTA from './SystemModeCTA.svelte';

  let selectedName = $state<string | null>(null);
  let peers = $state<PeerDetail[]>([]);
  let peersLoading = $state(false);
  let peersError = $state<string | null>(null);
  let toggling = $state(false);
  let toggleError = $state<string | null>(null);

  let enrollments = $derived(agent.status?.enrollments ?? []);

  async function toggleConnect(e: EnrollmentStatus) {
    toggling = true;
    toggleError = null;
    try {
      if (e.connected) {
        await local.disconnect(e.name);
      } else {
        await local.connect(e.name);
      }
      await agent.refresh();
    } catch (err: unknown) {
      toggleError = err instanceof Error ? err.message : String(err);
    } finally {
      toggling = false;
    }
  }

  $effect(() => {
    if (!selectedName && enrollments.length > 0) {
      selectedName = enrollments[0].name;
    }
  });

  // Peers list: fetch immediately on selection change AND poll every
  // 5s while the component is mounted. The agent's heartbeat /
  // peer-state aggregation runs on a similar cadence, so this is the
  // smallest interval that surfaces real updates without churning
  // the UI. Polling, not SSE: the agent doesn't currently push peer-
  // detail updates over the event stream.
  $effect(() => {
    if (!selectedName) return;
    const name = selectedName;

    let cancelled = false;

    const fetchOnce = async (showSpinner: boolean) => {
      if (showSpinner) peersLoading = true;
      try {
        const r = await local.peers(name);
        if (cancelled) return;
        peers = r.peers;
        peersError = null;
      } catch (e: unknown) {
        if (cancelled) return;
        peersError = e instanceof Error ? e.message : String(e);
        peers = [];
      } finally {
        if (!cancelled && showSpinner) peersLoading = false;
      }
    };

    void fetchOnce(true);
    const handle = window.setInterval(() => void fetchOnce(false), 5000);

    return () => {
      cancelled = true;
      window.clearInterval(handle);
    };
  });

  function selected(): EnrollmentStatus | null {
    return enrollments.find((e) => e.name === selectedName) ?? null;
  }

  function statusBadge(e: EnrollmentStatus) {
    // Plain-language labels (NN/g jargon rule). "P2P" was always
    // dropped — relay vs direct matters for performance, but a
    // novice doesn't need a protocol acronym to know that. Use
    // "via relay" only when relayed-only — direct peers ARE the
    // happy path and don't need a special badge.
    if (!e.connected) return { label: 'disconnected', cls: 'bg-zinc-800 text-zinc-400' };
    if (e.peersDirect > 0)
      return { label: 'connected', cls: 'bg-emerald-500/10 text-emerald-400' };
    if (e.peersRelayed > 0)
      return { label: 'connected · via relay', cls: 'bg-amber-500/10 text-amber-400' };
    return { label: 'connected', cls: 'bg-emerald-500/10 text-emerald-400' };
  }
</script>

<div class="flex h-full flex-col">
  <!-- Network selector (only shown when more than one) -->
  {#if enrollments.length > 1}
    <div class="border-b border-zinc-800 px-3 py-2">
      <div class="flex items-center gap-1 overflow-x-auto">
        {#each enrollments as e}
          <button
            class={selectedName === e.name
              ? 'shrink-0 rounded-md bg-zinc-800 px-2.5 py-1 text-xs text-zinc-100'
              : 'shrink-0 rounded-md px-2.5 py-1 text-xs text-zinc-400 hover:bg-zinc-900 hover:text-zinc-200'}
            onclick={() => (selectedName = e.name)}
          >
            {e.name}
          </button>
        {/each}
      </div>
    </div>
  {/if}

  {#if selected()}
    {@const e = selected()!}
    {@const badge = statusBadge(e)}
    <div class="space-y-4 p-4">
      <!-- Hero -->
      <div class="rounded-lg border border-zinc-800 bg-zinc-900/40 p-4">
        <div class="flex items-start justify-between">
          <div>
            <div class="flex items-baseline gap-2">
              <h2 class="text-lg font-semibold">{e.name}</h2>
              <span class="text-xs text-zinc-500">{e.endpoint}</span>
            </div>
            <p class="mt-0.5 font-mono text-xs text-zinc-400">
              {e.nebulaIp ?? e.meshIp ?? '—'}
            </p>
            <!-- Phase EE F2: device-identity affordance. For a self-
                 hosted product the relevant identity is "which device,
                 against which control plane" — we surface the hostname
                 here so multi-device users can verify they're looking
                 at the right Mac in this network's roster. -->
            {#if agent.status?.hostname}
              <p class="mt-0.5 text-[10px] text-zinc-500">
                this device: <span class="font-mono text-zinc-400">{agent.status.hostname}</span>
              </p>
            {/if}
          </div>
          <span class={`shrink-0 rounded-md px-2 py-1 text-[11px] font-medium ${badge.cls}`}>
            {badge.label}
          </span>
        </div>

        <div class="mt-4 grid grid-cols-3 gap-3 text-xs">
          <div>
            <div class="text-[10px] uppercase tracking-wide text-zinc-500">Peers</div>
            <div class="mt-0.5 font-medium text-zinc-100">
              {e.peersDirect + e.peersRelayed}
            </div>
            <div class="text-[10px] text-zinc-500">
              {e.peersDirect} direct · {e.peersRelayed} relay
            </div>
          </div>
          <div>
            <div class="text-[10px] uppercase tracking-wide text-zinc-500">Cert</div>
            <div class="mt-0.5 font-medium text-zinc-100">
              {e.certExpiresIn ?? '—'}
            </div>
            <div class="text-[10px] text-zinc-500">until renewal</div>
          </div>
          <div>
            <div class="text-[10px] uppercase tracking-wide text-zinc-500">Networking</div>
            <div class="mt-0.5 font-medium text-zinc-100">
              {(e.tunMode ?? 'userspace') === 'kernel' ? 'system-wide' : 'in-app'}
            </div>
            <div class="text-[10px] text-zinc-500">
              port {e.listenPort ?? '—'}
            </div>
          </div>
        </div>

        {#if e.lastError}
          <div class="mt-3 rounded-md border border-amber-900/40 bg-amber-950/30 px-3 py-2 text-xs text-amber-300">
            {e.lastError}
          </div>
        {/if}

        <div class="mt-4 flex items-center gap-2">
          <button
            type="button"
            disabled={toggling}
            title={toggling
              ? 'Switching network state — please wait'
              : e.connected
              ? 'Disconnect from this network'
              : 'Connect to this network'}
            class={e.connected
              ? 'rounded-md border border-zinc-700 px-3 py-1.5 text-xs text-zinc-300 hover:border-amber-500 hover:text-amber-400 disabled:opacity-50'
              : 'rounded-md bg-emerald-500 px-3 py-1.5 text-xs font-medium text-zinc-950 hover:bg-emerald-400 disabled:opacity-60'}
            onclick={() => toggleConnect(e)}
          >
            {toggling ? '…' : e.connected ? 'Disconnect' : 'Connect'}
          </button>
          <button
            type="button"
            class="rounded-md bg-zinc-800 px-3 py-1.5 text-xs hover:bg-zinc-700"
            onclick={() => openExternal(e.endpoint)}
          >
            Open dashboard
          </button>
          {#if e.dnsDomain}
            <span class="text-[11px] text-zinc-500">
              mesh DNS: <code class="rounded bg-zinc-800 px-1 py-0.5 font-mono">{e.dnsDomain}</code>
            </span>
          {/if}
        </div>
        {#if toggleError}
          <div class="mt-2 rounded-md border border-amber-900/40 bg-amber-950/30 px-3 py-2 text-xs text-amber-300">
            {toggleError}
          </div>
        {/if}
      </div>

      <!-- System-mode upsell. Only renders when in bundled mode + at
           least one connected network + not dismissed within 7 days.
           Self-hides once user enables system mode or dismisses. -->
      <SystemModeCTA />

      <!-- Peers list -->
      <div class="rounded-lg border border-zinc-800 bg-zinc-900/40">
        <div class="flex items-center justify-between border-b border-zinc-800 px-4 py-2.5">
          <h3 class="text-xs font-semibold uppercase tracking-wide text-zinc-300">
            Peers
          </h3>
          <!-- "refreshing…" indicator removed: peer rows update in
               place every 5s, the visible cadence was noise (NN/g
               status indicator pattern). -->
        </div>
        {#if peersError}
          <div class="px-4 py-3 text-xs text-amber-400">{peersError}</div>
        {:else if peers.length === 0}
          <div class="px-4 py-6 text-center text-xs text-zinc-500">
            {e.connected
              ? 'No peers online yet.'
              : 'Network is offline. Click Connect to retry.'}
          </div>
        {:else}
          <ul class="divide-y divide-zinc-800">
            {#each peers as p}
              {@const inSystemMode = agent.status?.runMode === 'system'}
              {@const dnsNames = inSystemMode
                ? [...(p.dnsHostname ? [p.dnsHostname] : []), ...(p.customDnsNames ?? [])]
                : []}
              <li class="px-4 py-2.5 text-xs">
                <div class="flex items-center justify-between">
                  <div class="flex min-w-0 items-center gap-2">
                    <span
                      class={p.direct
                        ? 'inline-block h-2 w-2 shrink-0 rounded-full bg-emerald-400'
                        : 'inline-block h-2 w-2 shrink-0 rounded-full bg-amber-400'}
                    ></span>
                    <span class="truncate font-medium text-zinc-200">
                      {p.name || p.vpnAddr}
                    </span>
                    {#if p.direct}
                      <span class="shrink-0 text-[10px] text-zinc-500">direct</span>
                    {:else}
                      <span class="shrink-0 text-[10px] text-zinc-500">via relay</span>
                    {/if}
                  </div>
                  <div class="flex shrink-0 items-center gap-3 text-zinc-500">
                    {#if p.rttMs}
                      <span class="font-mono">{p.rttMs}ms</span>
                    {/if}
                    {#if p.remoteAddr}
                      <span class="font-mono text-[10px]">{p.remoteAddr}</span>
                    {/if}
                  </div>
                </div>
                <div class="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-0.5 pl-4 text-[10px] text-zinc-500">
                  {#if p.name}
                    <span class="font-mono">{p.vpnAddr}</span>
                  {/if}
                  {#each dnsNames as dns}
                    <span class="font-mono">{dns}</span>
                  {/each}
                </div>
              </li>
            {/each}
          </ul>
        {/if}
      </div>
    </div>
  {:else}
    <div class="flex h-full items-center justify-center text-sm text-zinc-500">
      No networks yet. Click "Add network" above to connect this Mac.
    </div>
  {/if}
</div>
