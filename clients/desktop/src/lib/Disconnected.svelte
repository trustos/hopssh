<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { agent } from './stores.svelte';
  import { resetCachedEndpoint } from './local-api';

  let { error }: { error: string } = $props();

  const isTauri = typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window;

  // v0.10.87 (Phase W): the Retry button was previously a thin wrapper
  // around agent.refresh(), which against a Tauri shell that lost the
  // launch-time system-agent probe race always re-failed (state.endpoint
  // was None and nothing in refresh() re-probes). The new
  // retry_attach_system_agent Tauri command forces an explicit re-probe
  // of the mirror files + TCP connect, populating state.endpoint and
  // emitting agent-ready, before refresh() is called. The agent-ready
  // listener in stores.svelte.ts then resets the JS-layer endpoint
  // cache and re-subscribes to SSE.
  let retrying = $state(false);
  async function handleRetry() {
    retrying = true;
    try {
      if (isTauri) {
        try {
          const { invoke } = await import('@tauri-apps/api/core');
          await invoke('retry_attach_system_agent');
        } catch {
          // Either we're in bundled mode (no system mirror to re-probe)
          // or the retry attempt itself failed (daemon truly unreachable).
          // Fall through — agent.refresh() below at least re-attempts
          // the WebView fetch in case the underlying issue self-cleared.
          resetCachedEndpoint();
        }
      } else {
        resetCachedEndpoint();
      }
      await agent.refresh();
    } finally {
      retrying = false;
    }
  }

  // After CONNECTING_TIMEOUT_MS of failed connects, transition from the
  // friendly "Connecting…" spinner to the hard "Can't reach hop-agent"
  // message. SSE auto-retries with exponential backoff (500ms→1s→2s→4s→
  // 8s→10s capped) so transient blips of <10s should never show the
  // hard error state — that's just user-visible noise from a network
  // hiccup that's already self-healing.
  const CONNECTING_TIMEOUT_MS = 10_000;

  let now = $state(Date.now());
  let tickHandle: ReturnType<typeof setInterval> | null = null;
  onMount(() => {
    tickHandle = setInterval(() => (now = Date.now()), 500);
  });
  onDestroy(() => {
    if (tickHandle !== null) clearInterval(tickHandle);
  });

  // Inferred state. failedFor is null while the connection is healthy
  // (firstFailureAt is null) — but this component only renders when
  // agent.online is already false, so failedFor is effectively the
  // "how long has this been broken" value.
  let failedFor = $derived(
    agent.firstFailureAt === null ? 0 : now - agent.firstFailureAt
  );
  let stillRetrying = $derived(failedFor < CONNECTING_TIMEOUT_MS);
</script>

{#if stillRetrying}
  <!-- Friendly "we're working on it" state. SSE retry loop fires
       silently in the background; the user just sees a spinner and
       knows nothing is wrong yet. -->
  <div class="flex h-full flex-col items-center justify-center px-6 text-center">
    <div class="mb-4 inline-flex h-12 w-12 items-center justify-center rounded-full bg-zinc-800">
      <svg
        class="h-6 w-6 animate-spin text-emerald-400"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="2"
        stroke-linecap="round"
      >
        <path d="M21 12a9 9 0 1 1-6.219-8.56" />
      </svg>
    </div>
    <h2 class="mb-1 text-base font-semibold">Connecting…</h2>
    <p class="max-w-sm text-sm text-zinc-400">
      Reaching the hopssh agent on this Mac.
    </p>
  </div>
{:else}
  <!-- Hard error state — shown only after CONNECTING_TIMEOUT_MS. The
       user-visible text is intentionally jargon-free: no Tauri, no
       VITE_*, no .env.local. Real users get an action-oriented
       sentence + a Retry button. The detailed error message stays
       collapsed under "Show details" for support cases. -->
  <div class="flex h-full flex-col items-center justify-center px-6 text-center">
    <div class="mb-4 inline-flex h-12 w-12 items-center justify-center rounded-full bg-zinc-800">
      <svg
        class="h-6 w-6 text-amber-400"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        stroke-width="1.8"
        stroke-linecap="round"
        stroke-linejoin="round"
      >
        <path d="M12 9v4M12 17h.01M4.93 19h14.14a2 2 0 0 0 1.74-3L13.74 4a2 2 0 0 0-3.48 0L3.2 16a2 2 0 0 0 1.74 3z" />
      </svg>
    </div>
    <h2 class="mb-1 text-base font-semibold">hopssh isn't running</h2>
    <p class="mb-4 max-w-sm text-sm text-zinc-400">
      hopssh's background service didn't respond. Click Retry, or quit
      and reopen hopssh.
    </p>
    <button
      class="rounded-md bg-emerald-500 px-3 py-1.5 text-xs font-medium text-zinc-950 hover:bg-emerald-400 disabled:opacity-60"
      onclick={handleRetry}
      disabled={retrying}
    >
      {retrying ? 'Retrying…' : 'Retry'}
    </button>
    <details class="mt-6 max-w-sm text-[11px] text-zinc-500">
      <summary class="cursor-pointer hover:text-zinc-300">Show details</summary>
      <div class="mt-2 rounded-md bg-zinc-900 p-2 text-left font-mono text-[10px] leading-relaxed text-zinc-400">
        {error}
      </div>
    </details>
  </div>
{/if}
