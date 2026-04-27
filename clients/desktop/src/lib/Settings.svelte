<script lang="ts">
  import { onMount } from 'svelte';
  import { invoke } from '@tauri-apps/api/core';
  import { agent } from './stores.svelte';
  import { local } from './local-api';

  let leavingName = $state<string | null>(null);
  let leaveError = $state<string | null>(null);
  let leaveDone = $state<string | null>(null);
  let confirmingName = $state<string | null>(null);

  // Install integrations state.
  let installStatus = $state<{ system_service: boolean; cli_symlink: boolean } | null>(null);
  let installBusy = $state<string | null>(null); // which command is running
  let installResult = $state<{ kind: 'ok' | 'err'; msg: string } | null>(null);

  const isTauri = typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window;

  async function refreshInstallStatus() {
    if (!isTauri) return;
    try {
      installStatus = await invoke('install_status');
    } catch {
      installStatus = null;
    }
  }

  async function runInstall(cmd: string, label: string) {
    if (!isTauri) return;
    installBusy = label;
    installResult = null;
    try {
      const out = await invoke<string>(cmd);
      installResult = { kind: 'ok', msg: out };
      await refreshInstallStatus();
    } catch (e: unknown) {
      installResult = { kind: 'err', msg: e instanceof Error ? e.message : String(e) };
    } finally {
      installBusy = null;
    }
  }

  onMount(() => {
    refreshInstallStatus();
  });

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

  {#if isTauri}
    <section class="mt-6 rounded-lg border border-zinc-800 bg-zinc-900/40">
      <div class="border-b border-zinc-800 px-4 py-2.5">
        <h3 class="text-xs font-semibold uppercase tracking-wide text-zinc-300">
          System integrations
        </h3>
      </div>

      <!-- Kernel-TUN system service -->
      <div class="border-b border-zinc-800 px-4 py-3">
        <div class="flex items-start justify-between gap-3">
          <div class="min-w-0 flex-1">
            <div class="text-sm font-medium">Kernel-TUN system service</div>
            <p class="mt-0.5 text-[11px] leading-relaxed text-zinc-400">
              Runs hop-agent as a launchd daemon so the mesh stays up across
              user logouts and supports kernel-mode TUN (better for AV apps
              like Screen Sharing). Triggers a one-time admin prompt.
            </p>
            {#if installStatus}
              <div class="mt-1 text-[11px] {installStatus.system_service ? 'text-emerald-400' : 'text-zinc-500'}">
                {installStatus.system_service ? '● Installed' : '○ Not installed'}
              </div>
            {/if}
          </div>
          <div class="flex shrink-0 gap-2">
            {#if installStatus?.system_service}
              <button
                class="rounded-md border border-zinc-700 px-3 py-1.5 text-xs hover:border-red-500 hover:text-red-400 disabled:opacity-50"
                onclick={() => runInstall('uninstall_system_service', 'uninstall-svc')}
                disabled={installBusy !== null}
                type="button"
              >
                {installBusy === 'uninstall-svc' ? '…' : 'Uninstall'}
              </button>
            {:else}
              <button
                class="rounded-md bg-emerald-500 px-3 py-1.5 text-xs font-medium text-zinc-950 hover:bg-emerald-400 disabled:opacity-60"
                onclick={() => runInstall('install_system_service', 'install-svc')}
                disabled={installBusy !== null}
                type="button"
              >
                {installBusy === 'install-svc' ? 'Installing…' : 'Install'}
              </button>
            {/if}
          </div>
        </div>
      </div>

      <!-- CLI symlink -->
      <div class="px-4 py-3">
        <div class="flex items-start justify-between gap-3">
          <div class="min-w-0 flex-1">
            <div class="text-sm font-medium">Command line tools</div>
            <p class="mt-0.5 text-[11px] leading-relaxed text-zinc-400">
              Symlinks
              <code class="rounded bg-zinc-900 px-1 py-0.5 font-mono">/usr/local/bin/hop</code>
              to the bundled agent so you can run
              <code class="rounded bg-zinc-900 px-1 py-0.5 font-mono">hop status</code>
              from Terminal.
            </p>
            {#if installStatus}
              <div class="mt-1 text-[11px] {installStatus.cli_symlink ? 'text-emerald-400' : 'text-zinc-500'}">
                {installStatus.cli_symlink ? '● Installed' : '○ Not installed'}
              </div>
            {/if}
          </div>
          <div class="flex shrink-0 gap-2">
            {#if installStatus?.cli_symlink}
              <button
                class="rounded-md border border-zinc-700 px-3 py-1.5 text-xs hover:border-red-500 hover:text-red-400 disabled:opacity-50"
                onclick={() => runInstall('uninstall_cli_symlink', 'uninstall-cli')}
                disabled={installBusy !== null}
                type="button"
              >
                {installBusy === 'uninstall-cli' ? '…' : 'Uninstall'}
              </button>
            {:else}
              <button
                class="rounded-md bg-emerald-500 px-3 py-1.5 text-xs font-medium text-zinc-950 hover:bg-emerald-400 disabled:opacity-60"
                onclick={() => runInstall('install_cli_symlink', 'install-cli')}
                disabled={installBusy !== null}
                type="button"
              >
                {installBusy === 'install-cli' ? 'Installing…' : 'Install'}
              </button>
            {/if}
          </div>
        </div>
      </div>

      {#if installResult}
        <div
          class={installResult.kind === 'ok'
            ? 'border-t border-zinc-800 px-4 py-2 text-xs text-emerald-400'
            : 'border-t border-zinc-800 px-4 py-2 text-xs text-amber-400'}
        >
          {installResult.msg}
        </div>
      {/if}
    </section>
  {/if}

  <p class="mt-6 text-[11px] leading-relaxed text-zinc-500">
    Tip: hopssh runs in user-space mode by default — no admin prompts. The
    "Kernel-TUN system service" upgrade above gives better Screen Sharing
    fidelity at the cost of one admin prompt.
  </p>
</div>
