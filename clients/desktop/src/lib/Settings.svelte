<script lang="ts">
  import { agent } from './stores.svelte';
  import { local } from './local-api';

  let leavingName = $state<string | null>(null);
  let leaveError = $state<string | null>(null);
  let leaveDone = $state<string | null>(null);
  let confirmingName = $state<string | null>(null);

  async function doLeave(name: string) {
    leavingName = name;
    leaveError = null;
    try {
      const r = await local.leave(name);
      leaveDone = `Left ${r.removed}.`;
      await agent.refresh();
    } catch (e: unknown) {
      leaveError = e instanceof Error ? e.message : String(e);
    } finally {
      leavingName = null;
      confirmingName = null;
    }
  }
</script>

<div class="mx-auto max-w-md p-6">
  <h2 class="text-base font-semibold">Settings</h2>

  <section class="mt-6 rounded-lg border border-zinc-800 bg-zinc-900/40">
    <div class="border-b border-zinc-800 px-4 py-2.5">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-zinc-300">
        Agent
      </h3>
    </div>
    <dl class="divide-y divide-zinc-800 text-xs">
      <div class="flex justify-between px-4 py-2">
        <dt class="text-zinc-400">Version</dt>
        <dd class="font-mono">{agent.status?.version ?? '—'}</dd>
      </div>
      <div class="flex justify-between px-4 py-2">
        <dt class="text-zinc-400">Commit</dt>
        <dd class="font-mono text-[10px] text-zinc-500">{agent.status?.commit ?? '—'}</dd>
      </div>
      <div class="flex justify-between px-4 py-2">
        <dt class="text-zinc-400">OS / Arch</dt>
        <dd class="font-mono">{agent.status?.os}/{agent.status?.arch}</dd>
      </div>
      <div class="flex justify-between px-4 py-2">
        <dt class="text-zinc-400">Service</dt>
        <dd>{agent.status?.serviceStatus ?? '—'}</dd>
      </div>
      <div class="flex justify-between px-4 py-2">
        <dt class="text-zinc-400">Config</dt>
        <dd class="font-mono text-[10px]">{agent.status?.configDir}</dd>
      </div>
    </dl>
  </section>

  <section class="mt-6 rounded-lg border border-zinc-800 bg-zinc-900/40">
    <div class="border-b border-zinc-800 px-4 py-2.5">
      <h3 class="text-xs font-semibold uppercase tracking-wide text-zinc-300">
        Networks
      </h3>
    </div>
    {#if !agent.status || agent.status.enrollments.length === 0}
      <div class="px-4 py-4 text-xs text-zinc-500">
        Not connected to any network yet.
      </div>
    {:else}
      <ul class="divide-y divide-zinc-800">
        {#each agent.status.enrollments as e}
          <li class="px-4 py-3">
            <div class="flex items-start justify-between">
              <div>
                <div class="text-sm font-medium">{e.name}</div>
                <div class="mt-0.5 text-[11px] text-zinc-500">{e.endpoint}</div>
              </div>
              {#if confirmingName === e.name}
                <div class="flex gap-2">
                  <button
                    class="rounded-md bg-red-600 px-2 py-1 text-[11px] font-medium hover:bg-red-500 disabled:opacity-50"
                    onclick={() => doLeave(e.name)}
                    disabled={leavingName === e.name}
                    type="button"
                  >
                    {leavingName === e.name ? 'Leaving…' : 'Confirm'}
                  </button>
                  <button
                    class="rounded-md border border-zinc-700 px-2 py-1 text-[11px] hover:bg-zinc-800"
                    onclick={() => (confirmingName = null)}
                    type="button"
                  >
                    Cancel
                  </button>
                </div>
              {:else}
                <button
                  class="rounded-md border border-zinc-700 px-2 py-1 text-[11px] text-zinc-300 hover:border-red-500 hover:text-red-400"
                  onclick={() => (confirmingName = e.name)}
                  type="button"
                >
                  Leave
                </button>
              {/if}
            </div>
          </li>
        {/each}
      </ul>
    {/if}
    {#if leaveError}
      <div class="border-t border-zinc-800 px-4 py-2 text-xs text-amber-400">
        {leaveError}
      </div>
    {/if}
    {#if leaveDone}
      <div class="border-t border-zinc-800 px-4 py-2 text-xs text-emerald-400">
        {leaveDone}
      </div>
    {/if}
  </section>

  <p class="mt-6 text-[11px] leading-relaxed text-zinc-500">
    Tip: hopssh runs in user-space mode by default — no admin prompts. To
    upgrade to kernel-mode TUN (better for AV apps like Screen Sharing),
    install hop-agent as a system service from your terminal:
    <code class="rounded bg-zinc-900 px-1 py-0.5 font-mono">sudo hop-agent install</code>.
  </p>
</div>
