<script lang="ts">
  import { agent } from './stores.svelte';
  import { installPendingUpdate, pendingUpdateVersion } from './updater.svelte';

  let installing = $state(false);
  async function applyUpdate(id: number) {
    installing = true;
    try {
      await installPendingUpdate();
    } finally {
      installing = false;
      agent.dismissBanner(id);
    }
  }

  // Translate protocol-y banner titles emitted by the agent into
  // plain-English equivalents. Default-passthrough — anything not
  // in the map renders as-is, so the agent can still surface
  // brand-new alert types without a frontend change. New entries
  // should be added here whenever a stuck-data-plane / handshake-
  // timeout / similar-jargon banner ships agent-side.
  const titleHumanMap: Record<string, string> = {
    'data-plane stuck — recovering': 'Connection paused — reconnecting',
    'data-plane stuck': 'Connection paused',
    'handshake timeout': "Couldn't reach a peer — retrying",
    'cert renewal failed': "Couldn't refresh security keys — will retry",
    'agent unreachable': 'hopssh helper not responding',
  };
  function humanizeTitle(raw: string): string {
    return titleHumanMap[raw] ?? raw;
  }
</script>

{#if agent.banners.length > 0}
  <div class="flex shrink-0 flex-col gap-1 border-b border-zinc-800 bg-zinc-950 p-2">
    {#each agent.banners as b (b.id)}
      <div
        class={
          b.kind === 'error'
            ? 'flex items-start gap-2 rounded-md border border-red-900/60 bg-red-950/40 px-3 py-2 text-xs text-red-200'
            : b.kind === 'warning'
              ? 'flex items-start gap-2 rounded-md border border-amber-900/60 bg-amber-950/40 px-3 py-2 text-xs text-amber-200'
              : 'flex items-start gap-2 rounded-md border border-emerald-900/60 bg-emerald-950/40 px-3 py-2 text-xs text-emerald-200'
        }
      >
        <span class="mt-0.5 inline-block h-2 w-2 shrink-0 rounded-full"
          class:bg-red-400={b.kind === 'error'}
          class:bg-amber-400={b.kind === 'warning'}
          class:bg-emerald-400={b.kind === 'info'}
        ></span>
        <div class="min-w-0 flex-1">
          <div class="font-medium">{humanizeTitle(b.title)}</div>
          {#if b.detail}
            <div class="mt-0.5 text-[11px] text-zinc-400">{b.detail}</div>
          {/if}
        </div>
        {#if b.id < 0 && pendingUpdateVersion()}
          <button
            type="button"
            class="shrink-0 rounded-md bg-emerald-500 px-2.5 py-1 text-[11px] font-medium text-zinc-950 hover:bg-emerald-400 disabled:opacity-60"
            onclick={() => applyUpdate(b.id)}
            disabled={installing}
          >
            {installing ? 'Installing…' : 'Install + restart'}
          </button>
        {/if}
        <button
          type="button"
          aria-label="Dismiss"
          class="shrink-0 rounded-md px-1 text-zinc-500 hover:text-zinc-200"
          onclick={() => agent.dismissBanner(b.id)}
        >
          ×
        </button>
      </div>
    {/each}
  </div>
{/if}
