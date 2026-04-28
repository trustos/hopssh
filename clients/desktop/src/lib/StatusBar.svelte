<script lang="ts">
  import { agent } from './stores.svelte';

  let connected = $derived(
    agent.status?.enrollments.some((e) => e.connected) ?? false
  );
  let totalPeers = $derived(
    agent.status?.enrollments.reduce(
      (acc, e) => acc + e.peersDirect + e.peersRelayed,
      0
    ) ?? 0
  );
</script>

<footer
  class="flex shrink-0 items-center justify-between border-t border-zinc-800 bg-zinc-900/40 px-3 py-1.5 text-[11px] text-zinc-400"
>
  <!-- Left side intentionally compact when offline: the main view
       (Disconnected.svelte) is already showing a clear "Connecting…"
       or "hopssh isn't running" state, so duplicating "agent offline"
       here is just visual noise. When online, show network status. -->
  <div class="flex items-center gap-2">
    {#if agent.online}
      <span
        class="inline-block h-2 w-2 rounded-full bg-emerald-400"
        title="agent online"
      ></span>
      <span>agent online</span>
    {:else}
      <span class="inline-block h-2 w-2 rounded-full bg-zinc-700"></span>
    {/if}
  </div>
  <div class="flex items-center gap-3">
    {#if agent.online && connected}
      <span class="text-emerald-400">● connected</span>
    {:else if agent.online && agent.status?.enrollments.length}
      <span class="text-zinc-500">○ disconnected</span>
    {/if}
    {#if agent.online && totalPeers > 0}
      <span>{totalPeers} peer{totalPeers === 1 ? '' : 's'}</span>
    {/if}
  </div>
</footer>
