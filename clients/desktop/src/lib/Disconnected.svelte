<script lang="ts">
  import { agent } from './stores.svelte';

  let { error }: { error: string } = $props();
</script>

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
  <h2 class="mb-1 text-base font-semibold">Can't reach hop-agent</h2>
  <p class="mb-4 max-w-sm text-sm text-zinc-400">{error}</p>
  <button
    class="rounded-md bg-zinc-800 px-3 py-1.5 text-xs font-medium hover:bg-zinc-700"
    onclick={() => agent.refresh()}
  >
    Retry
  </button>
  <p class="mt-6 max-w-sm text-[11px] text-zinc-500">
    The desktop app talks to a local hop-agent via 127.0.0.1. Make sure the
    agent is running. In Tauri builds it's spawned automatically; in dev
    mode set <code class="rounded bg-zinc-800 px-1">VITE_HOPSSH_API</code>
    + <code class="rounded bg-zinc-800 px-1">VITE_HOPSSH_TOKEN</code>
    in <code class="rounded bg-zinc-800 px-1">.env.local</code>.
  </p>
</div>
