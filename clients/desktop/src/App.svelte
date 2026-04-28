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

  function tabClass(active: boolean, disabled = false) {
    if (disabled) {
      return 'rounded-md px-2.5 py-1 text-zinc-600 cursor-not-allowed';
    }
    return active
      ? 'rounded-md bg-zinc-800 px-2.5 py-1 text-zinc-100'
      : 'rounded-md px-2.5 py-1 text-zinc-400 hover:bg-zinc-900 hover:text-zinc-200';
  }

  // Explicit window-drag handler. Belt-and-braces alongside the
  // header's `data-tauri-drag-region` attribute: on macOS Sequoia +
  // Tauri 2.10 we've observed the data-attribute path miss the drag
  // initiation when the click lands on a child element (logo / span /
  // empty padding inside `pl-[78px]`). Calling getCurrentWindow().
  // startDragging() programmatically from a mousedown handler bypasses
  // the WebView's quirks and goes straight to the OS-level drag API.
  //
  // Skipped when the click target is interactive (button, input, anchor)
  // so tab-switching still works. Skipped on right-click — only the
  // primary mouse button initiates a drag.
  async function onHeaderMousedown(e: MouseEvent) {
    if (!('__TAURI_INTERNALS__' in window)) return;
    if (e.button !== 0) return;
    const target = e.target as HTMLElement | null;
    if (!target) return;
    // Don't drag when the click lands on something the user wants to
    // click (tabs, links, form controls). Walking up via closest()
    // handles nested elements (e.g. icons inside buttons).
    if (target.closest('button, a, input, select, textarea, [role="button"]')) {
      return;
    }
    try {
      const { getCurrentWindow } = await import('@tauri-apps/api/window');
      await getCurrentWindow().startDragging();
    } catch {
      // benign — falls back to the data-tauri-drag-region path.
    }
  }
</script>

<div class="flex h-full flex-col bg-zinc-950 text-zinc-100">
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <header
    class="flex shrink-0 items-center justify-between border-b border-zinc-800 py-3 pr-4 pl-[78px]"
    data-tauri-drag-region
    onmousedown={onHeaderMousedown}
  >
    <!-- pl-[78px] reserves space for macOS traffic lights (red/yellow/green)
         which sit at top-left of the window. tauri.conf.json sets
         titleBarStyle=Overlay + hiddenTitle so the lights float over our
         own header rather than living in a system-drawn title bar. -->
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
      <button
        class={tabClass(view === 'onboarding', !agent.online)}
        onclick={() => agent.online && (view = 'onboarding')}
        disabled={!agent.online}
        title={!agent.online ? 'Available once the agent is connected' : undefined}
      >
        Add
      </button>
      <button
        class={tabClass(view === 'settings', !agent.online)}
        onclick={() => agent.online && (view = 'settings')}
        disabled={!agent.online}
        title={!agent.online ? 'Available once the agent is connected' : undefined}
      >
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
