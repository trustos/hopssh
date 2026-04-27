<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { listen, type UnlistenFn } from '@tauri-apps/api/event';
  import { agent } from './lib/stores.svelte';
  import Onboarding from './lib/Onboarding.svelte';
  import Connected from './lib/Connected.svelte';
  import Settings from './lib/Settings.svelte';
  import StatusBar from './lib/StatusBar.svelte';
  import Disconnected from './lib/Disconnected.svelte';
  import Logo from './lib/Logo.svelte';
  import BannerStrip from './lib/BannerStrip.svelte';
  import { checkForUpdate } from './lib/updater';

  let view = $state<'main' | 'onboarding' | 'settings'>('main');
  let trayUnsub: UnlistenFn | null = null;

  onMount(() => {
    agent.start();
    if ('__TAURI_INTERNALS__' in window) {
      void listen<string>('tray-action', (e) => {
        if (e.payload === 'add') view = 'onboarding';
      }).then((u) => {
        trayUnsub = u;
      });
      // Probe for updates 10s after launch (after the agent + UI
      // settle). Errors are swallowed inside checkForUpdate.
      window.setTimeout(() => {
        void checkForUpdate();
      }, 10_000);
    }
  });
  onDestroy(() => {
    agent.stop();
    trayUnsub?.();
  });

  // Auto-route to onboarding when no enrollments exist.
  $effect(() => {
    if (
      !agent.initialLoad &&
      agent.status &&
      agent.status.enrollments.length === 0 &&
      view === 'main'
    ) {
      view = 'onboarding';
    }
  });

  function tabClass(active: boolean) {
    return active
      ? 'rounded-md bg-zinc-800 px-2.5 py-1 text-zinc-100'
      : 'rounded-md px-2.5 py-1 text-zinc-400 hover:bg-zinc-900 hover:text-zinc-200';
  }
</script>

<div class="flex h-full flex-col bg-zinc-950 text-zinc-100">
  <header
    class="flex shrink-0 items-center justify-between border-b border-zinc-800 px-4 py-3"
    data-tauri-drag-region
  >
    <div class="flex items-center gap-2">
      <Logo class="h-5 w-5 text-emerald-400" />
      <span class="text-sm font-semibold tracking-tight">hopssh</span>
      {#if agent.status?.version}
        <span class="text-[10px] text-zinc-500">v{agent.status.version}</span>
      {/if}
    </div>
    <nav class="flex items-center gap-1 text-xs">
      <button class={tabClass(view === 'main')} onclick={() => (view = 'main')}>
        Status
      </button>
      <button class={tabClass(view === 'onboarding')} onclick={() => (view = 'onboarding')}>
        Add
      </button>
      <button class={tabClass(view === 'settings')} onclick={() => (view = 'settings')}>
        Settings
      </button>
    </nav>
  </header>

  <BannerStrip />

  <main class="min-h-0 flex-1 overflow-y-auto">
    {#if agent.initialLoad}
      <div class="flex h-full items-center justify-center text-sm text-zinc-500">
        Connecting to agent…
      </div>
    {:else if !agent.online}
      <Disconnected error={agent.lastError ?? 'agent unreachable'} />
    {:else if view === 'onboarding'}
      <Onboarding onDone={() => (view = 'main')} />
    {:else if view === 'settings'}
      <Settings />
    {:else}
      <Connected />
    {/if}
  </main>

  <StatusBar />
</div>
