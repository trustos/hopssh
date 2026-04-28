<script lang="ts">
  import { invoke } from '@tauri-apps/api/core';
  import { agent } from './stores.svelte';
  import { local } from './local-api';

  // ---- Per-network "Leave" state (unchanged behavior) ----
  let leavingName = $state<string | null>(null);
  let leaveError = $state<string | null>(null);
  let leaveDone = $state<string | null>(null);
  let confirmingName = $state<string | null>(null);

  // ---- Danger zone state ----
  // Two-stage confirm pattern: first click flips to 'confirm', second
  // click commits. Mirrors the per-network Leave UX.
  let dangerConfirm = $state<'signout' | 'reset' | 'uninstall' | null>(null);
  let dangerBusy = $state<'signout' | 'reset' | 'uninstall' | null>(null);
  let dangerError = $state<string | null>(null);
  let uninstallDone = $state<string | null>(null);

  // ---- Background-mode toggle state ----
  // Drives the "Run in the background" preference. Reads runMode from
  // /local/status to infer the live state; calls convert/revert Tauri
  // commands on toggle.
  let bgConfirm = $state<'enable' | 'disable' | null>(null);
  let bgBusy = $state(false);
  let bgError = $state<string | null>(null);
  let bgDone = $state<string | null>(null);

  const isTauri = typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window;

  // True when the agent reports it's the launchd-spawned system daemon.
  // When false, the agent is a child of the .app (bundled mode).
  let inSystemMode = $derived(agent.status?.runMode === 'system');

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

  async function leaveAllNetworks(): Promise<{ leftCount: number; errors: string[] }> {
    const status = agent.status;
    if (!status) return { leftCount: 0, errors: [] };
    const errors: string[] = [];
    let leftCount = 0;
    for (const e of status.enrollments) {
      try {
        await local.leave(e.name);
        leftCount++;
      } catch (err) {
        errors.push(`${e.name}: ${err instanceof Error ? err.message : String(err)}`);
      }
    }
    await agent.refresh();
    return { leftCount, errors };
  }

  async function doDangerSignout() {
    dangerBusy = 'signout';
    dangerError = null;
    try {
      const { leftCount, errors } = await leaveAllNetworks();
      if (errors.length > 0) {
        dangerError = `Signed out of ${leftCount} network(s); errors: ${errors.join('; ')}`;
      } else {
        leaveDone = `Signed out of ${leftCount} network(s).`;
      }
    } finally {
      dangerBusy = null;
      dangerConfirm = null;
    }
  }

  async function doDangerReset() {
    dangerBusy = 'reset';
    dangerError = null;
    try {
      const { errors } = await leaveAllNetworks();
      if (errors.length > 0) {
        dangerError = `Some networks failed to leave: ${errors.join('; ')}. Continuing with reset.`;
      }
      const out = await invoke<string>('reset_hopssh');
      leaveDone = out;
      await agent.refresh();
    } catch (e: unknown) {
      dangerError = e instanceof Error ? e.message : String(e);
    } finally {
      dangerBusy = null;
      dangerConfirm = null;
    }
  }

  async function doDangerUninstall() {
    dangerBusy = 'uninstall';
    dangerError = null;
    try {
      await leaveAllNetworks();
      const out = await invoke<string>('uninstall_hopssh_full');
      uninstallDone = out;
    } catch (e: unknown) {
      dangerError = e instanceof Error ? e.message : String(e);
    } finally {
      dangerBusy = null;
      dangerConfirm = null;
    }
  }

  async function doQuitApp() {
    try {
      await invoke('quit_app');
    } catch (e: unknown) {
      dangerError = `Failed to quit: ${e instanceof Error ? e.message : String(e)}`;
    }
  }

  // ---- Background-mode handlers ----
  // Toggle ON: convert_to_system_service. The .app's bundled child
  // shuts down first, then admin prompt installs the LaunchDaemon and
  // migrates enrollments. ~5s mesh gap.
  async function doEnableBackground() {
    bgBusy = true;
    bgError = null;
    bgDone = null;
    try {
      const msg = await invoke<string>('convert_to_system_service');
      bgDone = msg;
      // Refresh status so runMode flips to 'system' in the UI.
      await agent.refresh();
    } catch (e: unknown) {
      bgError = e instanceof Error ? e.message : String(e);
    } finally {
      bgBusy = false;
      bgConfirm = null;
    }
  }

  // Toggle OFF: revert_to_bundled. The system LaunchDaemon stops +
  // unloads, enrollments move back into the user's configDir, and
  // the .app's bundled child re-spawns on the next refresh.
  async function doDisableBackground() {
    bgBusy = true;
    bgError = null;
    bgDone = null;
    try {
      const msg = await invoke<string>('revert_to_bundled');
      bgDone = msg;
      await agent.refresh();
    } catch (e: unknown) {
      bgError = e instanceof Error ? e.message : String(e);
    } finally {
      bgBusy = false;
      bgConfirm = null;
    }
  }
</script>

<div class="mx-auto max-w-md p-6">
  <h2 class="text-base font-semibold">Settings</h2>

  <!-- ============================================================
       Preferences — the user-intent toggles. Replaces the previous
       "System integrations" section with developer-jargon "Kernel-TUN
       system service" + "CLI symlink" buttons. Tailscale/ZeroTier
       convention: one toggle, "Run in the background", phrased in
       user terms.
       ============================================================ -->
  {#if isTauri}
    <section class="mt-6 rounded-lg border border-zinc-800 bg-zinc-900/40">
      <div class="border-b border-zinc-800 px-4 py-2.5">
        <h3 class="text-xs font-semibold uppercase tracking-wide text-zinc-300">
          Preferences
        </h3>
      </div>

      <div class="px-4 py-3">
        <div class="flex items-start justify-between gap-3">
          <div class="min-w-0 flex-1">
            <div class="text-sm font-medium">Run in the background</div>
            <p class="mt-0.5 text-[11px] leading-relaxed text-zinc-400">
              Keep hopssh connected when you log out, and reconnect after
              restart. Briefly disconnects (~5s) while switching modes.
              Triggers an admin prompt.
            </p>
            <div class="mt-1 text-[11px] {inSystemMode ? 'text-emerald-400' : 'text-zinc-500'}">
              {inSystemMode ? '● On' : '○ Off — only runs while hopssh is open'}
            </div>
          </div>
          <div class="flex shrink-0 gap-2">
            {#if bgConfirm !== null}
              <button
                class="rounded-md bg-emerald-500 px-2.5 py-1.5 text-xs font-medium text-zinc-950 hover:bg-emerald-400 disabled:opacity-50"
                onclick={bgConfirm === 'enable' ? doEnableBackground : doDisableBackground}
                disabled={bgBusy}
                type="button"
              >
                {bgBusy ? 'Working…' : 'Confirm'}
              </button>
              <button
                class="rounded-md border border-zinc-700 px-2.5 py-1.5 text-xs hover:bg-zinc-800"
                onclick={() => (bgConfirm = null)}
                type="button"
                disabled={bgBusy}
              >
                Cancel
              </button>
            {:else if inSystemMode}
              <button
                class="rounded-md border border-zinc-700 px-3 py-1.5 text-xs text-zinc-300 hover:border-amber-500 hover:text-amber-400 disabled:opacity-50"
                onclick={() => (bgConfirm = 'disable')}
                disabled={bgBusy}
                type="button"
              >
                Turn off
              </button>
            {:else}
              <button
                class="rounded-md bg-emerald-500 px-3 py-1.5 text-xs font-medium text-zinc-950 hover:bg-emerald-400 disabled:opacity-60"
                onclick={() => (bgConfirm = 'enable')}
                disabled={bgBusy}
                type="button"
              >
                Turn on
              </button>
            {/if}
          </div>
        </div>
      </div>

      {#if bgError}
        <div class="border-t border-zinc-800 px-4 py-2 text-xs text-amber-400">
          {bgError}
        </div>
      {/if}
      {#if bgDone}
        <div class="border-t border-zinc-800 px-4 py-2 text-xs text-emerald-400">
          {bgDone}
        </div>
      {/if}
    </section>
  {/if}

  <!-- ============================================================
       Networks — per-enrollment Leave (unchanged).
       ============================================================ -->
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
              <div class="min-w-0 flex-1">
                <div class="flex items-center gap-2 text-sm font-medium">
                  <span
                    class={
                      e.connected
                        ? 'inline-block h-2 w-2 shrink-0 rounded-full bg-emerald-400'
                        : 'inline-block h-2 w-2 shrink-0 rounded-full bg-zinc-600'
                    }
                    title={e.connected ? 'connected' : 'disconnected'}
                  ></span>
                  <span class="truncate">{e.name}</span>
                </div>
                <div class="mt-0.5 truncate text-[11px] text-zinc-500">{e.endpoint}</div>
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

  <!-- ============================================================
       Danger zone — Sign out / Reset / Uninstall.
       Replaced by the post-uninstall banner once Uninstall succeeds.
       ============================================================ -->
  {#if isTauri}
    {#if uninstallDone}
      <section class="mt-6 rounded-lg border border-emerald-800 bg-emerald-950/40 p-4">
        <h3 class="text-sm font-semibold text-emerald-200">Uninstall complete</h3>
        <p class="mt-2 text-xs leading-relaxed text-emerald-100/90">
          To finish removing hopssh from this Mac, quit the app and drag
          <span class="font-semibold">hopssh.app</span> from
          <span class="font-mono">/Applications</span> to the Trash.
        </p>
        <button
          class="mt-3 rounded-md bg-emerald-500 px-3 py-1.5 text-xs font-medium text-zinc-950 hover:bg-emerald-400"
          onclick={doQuitApp}
          type="button"
        >
          Quit hopssh
        </button>
      </section>
    {:else}
      <section class="mt-6 rounded-lg border border-red-900/60 bg-red-950/20">
        <div class="border-b border-red-900/60 px-4 py-2.5">
          <h3 class="text-xs font-semibold uppercase tracking-wide text-red-300">
            Danger zone
          </h3>
        </div>

        <div class="border-b border-red-900/60 px-4 py-3">
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0 flex-1">
              <div class="text-sm font-medium">Sign out of all networks</div>
              <p class="mt-0.5 text-[11px] leading-relaxed text-zinc-400">
                Disconnects every enrollment. Re-enroll anytime — no admin
                prompt. {agent.status?.enrollments.length ?? 0} active.
              </p>
            </div>
            <div class="flex shrink-0 gap-2">
              {#if dangerConfirm === 'signout'}
                <button
                  class="rounded-md bg-red-600 px-2.5 py-1.5 text-xs font-medium hover:bg-red-500 disabled:opacity-50"
                  onclick={doDangerSignout}
                  disabled={dangerBusy !== null}
                  type="button"
                >
                  {dangerBusy === 'signout' ? 'Signing out…' : 'Confirm'}
                </button>
                <button
                  class="rounded-md border border-zinc-700 px-2.5 py-1.5 text-xs hover:bg-zinc-800"
                  onclick={() => (dangerConfirm = null)}
                  type="button"
                >
                  Cancel
                </button>
              {:else}
                <button
                  class="rounded-md border border-red-900/80 px-3 py-1.5 text-xs text-red-300 hover:border-red-500 hover:bg-red-950/40 disabled:opacity-50"
                  onclick={() => (dangerConfirm = 'signout')}
                  disabled={dangerBusy !== null || (agent.status?.enrollments.length ?? 0) === 0}
                  type="button"
                >
                  Sign out
                </button>
              {/if}
            </div>
          </div>
        </div>

        <div class="border-b border-red-900/60 px-4 py-3">
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0 flex-1">
              <div class="text-sm font-medium">Reset hopssh</div>
              <p class="mt-0.5 text-[11px] leading-relaxed text-zinc-400">
                Removes all enrollments + certificates. Keeps the app
                installed so you can re-enroll fresh. Logs preserved.
                Triggers an admin prompt.
              </p>
            </div>
            <div class="flex shrink-0 gap-2">
              {#if dangerConfirm === 'reset'}
                <button
                  class="rounded-md bg-red-600 px-2.5 py-1.5 text-xs font-medium hover:bg-red-500 disabled:opacity-50"
                  onclick={doDangerReset}
                  disabled={dangerBusy !== null}
                  type="button"
                >
                  {dangerBusy === 'reset' ? 'Resetting…' : 'Confirm reset'}
                </button>
                <button
                  class="rounded-md border border-zinc-700 px-2.5 py-1.5 text-xs hover:bg-zinc-800"
                  onclick={() => (dangerConfirm = null)}
                  type="button"
                >
                  Cancel
                </button>
              {:else}
                <button
                  class="rounded-md border border-red-900/80 px-3 py-1.5 text-xs text-red-300 hover:border-red-500 hover:bg-red-950/40 disabled:opacity-50"
                  onclick={() => (dangerConfirm = 'reset')}
                  disabled={dangerBusy !== null}
                  type="button"
                >
                  Reset
                </button>
              {/if}
            </div>
          </div>
        </div>

        <div class="px-4 py-3">
          <div class="flex items-start justify-between gap-3">
            <div class="min-w-0 flex-1">
              <div class="text-sm font-medium">Uninstall hopssh</div>
              <p class="mt-0.5 text-[11px] leading-relaxed text-zinc-400">
                Removes everything: enrollments, certs, the background
                service, and the agent binary. After confirm: drag
                <span class="font-mono">/Applications/hopssh.app</span>
                to the Trash. Triggers an admin prompt.
              </p>
            </div>
            <div class="flex shrink-0 gap-2">
              {#if dangerConfirm === 'uninstall'}
                <button
                  class="rounded-md bg-red-600 px-2.5 py-1.5 text-xs font-medium hover:bg-red-500 disabled:opacity-50"
                  onclick={doDangerUninstall}
                  disabled={dangerBusy !== null}
                  type="button"
                >
                  {dangerBusy === 'uninstall' ? 'Uninstalling…' : 'Confirm uninstall'}
                </button>
                <button
                  class="rounded-md border border-zinc-700 px-2.5 py-1.5 text-xs hover:bg-zinc-800"
                  onclick={() => (dangerConfirm = null)}
                  type="button"
                >
                  Cancel
                </button>
              {:else}
                <button
                  class="rounded-md border border-red-900/80 px-3 py-1.5 text-xs text-red-300 hover:border-red-500 hover:bg-red-950/40 disabled:opacity-50"
                  onclick={() => (dangerConfirm = 'uninstall')}
                  disabled={dangerBusy !== null}
                  type="button"
                >
                  Uninstall
                </button>
              {/if}
            </div>
          </div>
        </div>

        {#if dangerError}
          <div class="border-t border-red-900/60 px-4 py-2 text-xs text-amber-400">
            {dangerError}
          </div>
        {/if}
      </section>
    {/if}
  {/if}

  <!-- ============================================================
       About — diagnostic metadata, collapsed by default. Replaces the
       previous "Agent" panel that surfaced "Service: stopped" as a
       confusing first-class field.
       ============================================================ -->
  <details class="mt-6 rounded-lg border border-zinc-800 bg-zinc-900/40">
    <summary class="cursor-pointer px-4 py-2.5 text-xs font-semibold uppercase tracking-wide text-zinc-400 hover:text-zinc-200">
      About
    </summary>
    <dl class="border-t border-zinc-800 divide-y divide-zinc-800 text-xs">
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
        <dt class="text-zinc-400">Mode</dt>
        <dd class="font-mono">{inSystemMode ? 'system (background)' : 'bundled (.app)'}</dd>
      </div>
      <div class="flex flex-col px-4 py-2">
        <dt class="text-zinc-400">Config</dt>
        <dd class="mt-1 break-all font-mono text-[10px] text-zinc-500">
          {agent.status?.configDir}
        </dd>
      </div>
    </dl>
  </details>
</div>
