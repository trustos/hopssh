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
  <div class="flex items-center gap-2">
    <span
      class={agent.online
        ? 'inline-block h-2 w-2 rounded-full bg-emerald-400'
        : 'inline-block h-2 w-2 rounded-full bg-zinc-600'}
      title={agent.online ? 'agent online' : 'agent offline'}
    ></span>
    <span>
      {agent.online ? 'agent online' : 'agent offline'}
    </span>
  </div>
  <div class="flex items-center gap-3">
    {#if connected}
      <span class="text-emerald-400">● connected</span>
    {:else if agent.status?.enrollments.length}
      <span class="text-zinc-500">○ disconnected</span>
    {/if}
    {#if totalPeers > 0}
      <span>{totalPeers} peer{totalPeers === 1 ? '' : 's'}</span>
    {/if}
  </div>
</footer>
