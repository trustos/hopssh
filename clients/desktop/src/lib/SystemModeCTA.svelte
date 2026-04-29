<script lang="ts">
  import { invoke } from '@tauri-apps/api/core';
  import { agent } from './stores.svelte';

  // Re-prompt window for users who declined or never saw the bgprompt.
  // 7 days matches the research-cited cadence for non-intrusive
  // re-asks (NN/g, Apple HIG: don't pester, don't disappear).
  const DISMISS_KEY = 'hopssh.system-mode-cta-dismissed-at';
  const DISMISS_WINDOW_MS = 7 * 24 * 60 * 60 * 1000;

  let busy = $state(false);
  let error = $state('');
  let dismissedAt = $state<number | null>(
    typeof localStorage !== 'undefined'
      ? Number(localStorage.getItem(DISMISS_KEY)) || null
      : null
  );

  let runMode = $derived(agent.status?.runMode ?? 'bundled');
  let anyConnected = $derived(
    (agent.status?.enrollments ?? []).some((e) => e.connected)
  );
  let withinDismissWindow = $derived(
    dismissedAt !== null && Date.now() - dismissedAt < DISMISS_WINDOW_MS
  );

  // Show only when:
  // - we're in bundled mode (system mode = nothing to upsell)
  // - at least one network is connected (priming requires concrete value)
  // - user hasn't dismissed in the past 7 days
  let visible = $derived(
    runMode === 'bundled' && anyConnected && !withinDismissWindow
  );

  async function enable() {
    busy = true;
    error = '';
    try {
      await invoke<string>('convert_to_system_service');
      await agent.refresh();
      // On success, runMode flips to 'system' → component disappears
      // automatically via $derived. Clear dismissed-at so future
      // bundled-mode reverts get a fresh CTA cycle.
      localStorage.removeItem(DISMISS_KEY);
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : String(e);
      // "admin prompt cancelled by user" is the explicit marker from
      // run_osascript on user cancel. Soften the copy in that case;
      // anything else is a real failure worth surfacing verbatim.
      if (msg.includes('admin prompt cancelled') || msg.includes('User canceled')) {
        error = 'Cancelled. You can try again any time.';
      } else {
        error = msg;
      }
    } finally {
      busy = false;
    }
  }

  function dismiss() {
    const now = Date.now();
    localStorage.setItem(DISMISS_KEY, String(now));
    dismissedAt = now;
  }
</script>

{#if visible}
  <div class="rounded-lg border border-emerald-900/40 bg-emerald-950/30 p-4">
    <div class="flex items-start gap-3">
      <span class="mt-0.5 text-xl">⚡</span>
      <div class="min-w-0 flex-1">
        <h3 class="text-sm font-semibold text-emerald-100">
          Make hopssh work like Tailscale
        </h3>
        <p class="mt-1 text-[12px] leading-relaxed text-zinc-300">
          Right now <code class="rounded bg-zinc-900/60 px-1 font-mono">ping 10.42.x.x</code>
          from Terminal won't work — only hopssh-aware paths use the mesh.
          Installing a background service gives every app on your Mac
          system-wide access to mesh hostnames and IPs, exactly like
          Tailscale. Survives reboots and quits.
        </p>
        <p class="mt-1 text-[11px] text-zinc-500">
          Triggers a one-time admin prompt. Reversible from Settings → Preferences.
        </p>
        {#if error}
          <div class="mt-2 rounded-md border border-amber-900/40 bg-amber-950/30 px-3 py-2 text-[11px] text-amber-300">
            {error}
          </div>
        {/if}
        <div class="mt-3 flex gap-2">
          <button
            type="button"
            disabled={busy}
            class="rounded-md bg-emerald-500 px-3 py-1.5 text-xs font-medium text-zinc-950 hover:bg-emerald-400 disabled:opacity-50"
            onclick={enable}
          >
            {busy ? 'Setting up…' : 'Enable system-wide networking'}
          </button>
          <button
            type="button"
            disabled={busy}
            class="rounded-md border border-zinc-700 px-3 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800 disabled:opacity-50"
            onclick={dismiss}
          >
            Not now
          </button>
        </div>
      </div>
    </div>
  </div>
{/if}
