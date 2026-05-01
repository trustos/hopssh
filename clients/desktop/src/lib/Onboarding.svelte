<script lang="ts">
  import { onDestroy } from 'svelte';
  import { invoke } from '@tauri-apps/api/core';
  import { local } from './local-api';
  import { agent } from './stores.svelte';
  import { openExternal } from './tauri-bridge';

  let { onDone }: { onDone: () => void } = $props();

  // ---- Idle state (control-plane picker) ----
  // Hosted vs self-hosted radio replaces the free-text URL input. 99%
  // of users land on hopssh.com; the 1% with self-hosted picks "Other"
  // and types a URL. Last-used self-hosted URL is remembered so the
  // common second-time-self-hosted user re-enrolls in one click.
  type Mode = 'hosted' | 'selfhosted';
  const HOSTED_URL = 'https://hopssh.com';
  const STORAGE_KEY = 'hopssh.onboarding.lastSelfHosted';

  let endpointMode = $state<Mode>('hosted');
  let selfHostedUrl = $state(
    typeof localStorage !== 'undefined' ? localStorage.getItem(STORAGE_KEY) ?? '' : ''
  );

  let stage = $state<
    'idle' | 'starting' | 'pending' | 'completing' | 'bgprompt' | 'bgconverting' | 'done' | 'error'
  >('idle');
  let userCode = $state('');
  let verificationUrl = $state('');
  let pollDeadline = $state<number | null>(null);
  let errorMessage = $state('');
  let pollHandle: number | null = null;
  let showCode = $state(false); // disclosure for "approve from another device"

  function effectiveEndpoint(): string {
    return endpointMode === 'hosted' ? HOSTED_URL : selfHostedUrl.trim();
  }

  // No WebView-side pre-flight: a fetch from the WebView's origin
  // (http://tauri.localhost) to hopssh.com /healthz is blocked by CORS
  // (the public endpoint correctly does NOT advertise broad ACAO),
  // which the WebView surfaces as TypeError "Failed to fetch" — not
  // useful to the user and indistinguishable from a real network
  // outage. Instead, we trust the agent's server-to-server POST to
  // /api/device/code (no CORS, full error reporting) to surface
  // unreachability with a real diagnostic.

  async function startFlow() {
    const endpoint = effectiveEndpoint();
    if (!endpoint.match(/^https?:\/\//)) {
      errorMessage = 'URL must start with http:// or https://';
      stage = 'error';
      return;
    }
    if (endpointMode === 'selfhosted') {
      try {
        localStorage.setItem(STORAGE_KEY, endpoint);
      } catch {
        // benign — private mode etc.
      }
    }
    stage = 'starting';
    errorMessage = '';
    showCode = false;
    try {
      const r = await local.enrollDeviceFlowStart({ endpoint });
      userCode = r.userCode;
      verificationUrl = r.verificationUrl;
      pollDeadline = Date.now() + r.expiresIn * 1000;
      stage = 'pending';
      // Open the browser with the embedded ?code= URL — no manual paste
      // required when the user is signed in with a single admin network.
      void openExternal(verificationUrl);
      poll(r.deviceCode, r.interval * 1000);
    } catch (e: unknown) {
      errorMessage = e instanceof Error ? e.message : String(e);
      stage = 'error';
    }
  }

  async function poll(deviceCode: string, intervalMs: number) {
    const tick = async () => {
      if (stage !== 'pending') return;
      try {
        const r = await local.enrollDeviceFlowPoll(deviceCode);
        if (r.status === 'pending') {
          pollHandle = window.setTimeout(tick, intervalMs);
          return;
        }
        if (r.status === 'expired') {
          stage = 'error';
          errorMessage = 'Code expired. Try again.';
          return;
        }
        if (r.status === 'error') {
          stage = 'error';
          errorMessage = r.message ?? "Couldn't connect to this network";
          return;
        }
        if (r.status === 'complete') {
          stage = 'completing';
          await agent.refresh();
          // Decide whether to surface the post-enrollment "Run in the
          // background" prompt. We only ask once per Mac via a
          // localStorage flag — after the first decision the user can
          // change their mind anytime in Settings → Preferences.
          // Skip when already in system mode (re-enrolling on a Mac
          // that's already converted).
          const alreadyAsked = localStorage.getItem('hopssh.onboarding.bgPromptShown') === '1';
          const alreadyInSystemMode = agent.status?.runMode === 'system';
          if (!alreadyAsked && !alreadyInSystemMode) {
            stage = 'bgprompt';
          } else {
            stage = 'done';
            window.setTimeout(onDone, 1500);
          }
          return;
        }
      } catch (e: unknown) {
        if (pollDeadline && Date.now() > pollDeadline) {
          stage = 'error';
          errorMessage = e instanceof Error ? e.message : String(e);
          return;
        }
        pollHandle = window.setTimeout(tick, intervalMs);
      }
    };
    pollHandle = window.setTimeout(tick, intervalMs);
  }

  function cancel() {
    if (pollHandle !== null) {
      window.clearTimeout(pollHandle);
      pollHandle = null;
    }
    stage = 'idle';
    userCode = '';
    verificationUrl = '';
    pollDeadline = null;
    errorMessage = '';
    showCode = false;
  }

  onDestroy(() => {
    if (pollHandle !== null) window.clearTimeout(pollHandle);
  });

  function copyCodeWithPrefix() {
    void navigator.clipboard.writeText(userCode);
  }
  function copyCodeBare() {
    void navigator.clipboard.writeText(userCode.replace(/^HOP-/i, ''));
  }
  function reopenBrowser() {
    if (verificationUrl) void openExternal(verificationUrl);
  }

  // ---- Post-enrollment "Run in the background" prompt handlers ----
  // The decision is recorded so we don't nag on subsequent enrollments.
  // Same default both branches: dismiss to Status after a short success
  // hold time, mirroring the original onDone flow.
  let bgPromptError = $state('');
  async function acceptBackground() {
    bgPromptError = '';
    stage = 'bgconverting';
    try {
      // The convert flow: AppState.shutdown_agent() (kills bundled
      // child) → osascript admin prompt → migrate enrollments to
      // /etc/hop-agent → install LaunchDaemon → mirror token written
      // for the .app to pick up. ~5s gap.
      await invoke<string>('convert_to_system_service');
      await agent.refresh();
      localStorage.setItem('hopssh.onboarding.bgPromptShown', '1');
      stage = 'done';
      window.setTimeout(onDone, 1500);
    } catch (e: unknown) {
      const msg = e instanceof Error ? e.message : String(e);
      // "admin prompt cancelled by user" is the explicit marker from
      // run_osascript on user cancel — show a softer message that
      // makes "try again or stay in userspace" obvious. Anything else
      // is a real failure worth surfacing verbatim.
      if (msg.includes('admin prompt cancelled') || msg.includes('User canceled')) {
        bgPromptError = "Cancelled. You can try again, or skip — the mesh will still work inside hopssh and through the dashboard's web terminal.";
      } else {
        bgPromptError = msg;
      }
      // Stay on the bgprompt screen so the user can retry or decline.
      stage = 'bgprompt';
    }
  }
  function declineBackground() {
    localStorage.setItem('hopssh.onboarding.bgPromptShown', '1');
    stage = 'done';
    window.setTimeout(onDone, 1500);
  }

  // ---- Phase 2: parallel-install detection ----
  // The /local/status response carries parallelInstall when a leftover
  // hop-agent install is detected on this Mac. We surface a banner +
  // one-click Reset BEFORE enrollment so the user never hits the
  // port-bind conflict.
  let parallelInstall = $derived(agent.status?.parallelInstall ?? null);
  let resetting = $state(false);
  let resetError = $state('');
  async function runReset() {
    resetting = true;
    resetError = '';
    try {
      // uninstall_hopssh_full removes the leftover system install
      // entirely (LaunchDaemon + /etc/hop-agent + /usr/local/bin). The
      // bundled .app continues running because it lives in /Applications.
      // After this returns, parallelInstall in the status will clear on
      // next refresh.
      await invoke<string>('uninstall_hopssh_full');
      await agent.refresh();
    } catch (e: unknown) {
      resetError = e instanceof Error ? e.message : String(e);
    } finally {
      resetting = false;
    }
  }

  // ---- Progress ladder ----
  // Three steps the user can see at all times once enrollment kicks off.
  // Mirrors the steps shown on the browser /device page so users see a
  // consistent narrative across the desktop and the browser tabs.
  let steps = $derived([
    {
      label: 'Open browser',
      done: stage === 'pending' || stage === 'completing' || stage === 'done',
      active: stage === 'starting'
    },
    {
      label: 'Approve in browser',
      done: stage === 'completing' || stage === 'done',
      active: stage === 'pending'
    },
    {
      label: 'Bring mesh up',
      done: stage === 'done',
      active: stage === 'completing'
    }
  ]);
</script>

<div class="mx-auto max-w-md p-6">
  <h2 class="text-base font-semibold">Add a network</h2>

  {#if stage === 'idle' || stage === 'error'}
    <p class="mt-1 text-sm text-zinc-400">
      Sign in to connect this Mac to a hopssh network.
    </p>

    {#if parallelInstall && (parallelInstall.launchDaemon || parallelInstall.legacyConfigDir)}
      <!-- Parallel-install warning. Removing the leftover BEFORE enrollment
           avoids the "port already in use" trap entirely. -->
      <div class="mt-6 rounded-md border border-amber-900/60 bg-amber-950/40 p-4">
        <div class="text-sm font-medium text-amber-200">
          Another hopssh install detected
        </div>
        <p class="mt-1 text-[12px] leading-relaxed text-amber-100/80">
          A previous installation of hopssh is still on this Mac
          ({parallelInstall.launchDaemon ? 'system service' : ''}{parallelInstall.launchDaemon &&
          parallelInstall.legacyConfigDir
            ? ' + '
            : ''}{parallelInstall.legacyConfigDir ? 'system config files' : ''}).
          It will conflict with new networks. Remove it now to avoid
          a port collision.
        </p>
        {#if resetError}
          <div class="mt-2 text-[11px] text-red-300">{resetError}</div>
        {/if}
        <button
          type="button"
          class="mt-3 inline-flex items-center rounded-md bg-amber-500 px-3 py-1.5 text-xs font-medium text-zinc-950 hover:bg-amber-400 disabled:opacity-60"
          onclick={runReset}
          disabled={resetting}
        >
          {resetting ? 'Removing…' : 'Remove old install'}
        </button>
      </div>
    {/if}

    <form
      class="mt-6 space-y-4"
      onsubmit={(e) => {
        e.preventDefault();
        startFlow();
      }}
    >
      <fieldset class="space-y-2">
        <legend class="text-xs uppercase tracking-wide text-zinc-400">Where do you want to connect?</legend>
        <label class="flex cursor-pointer items-start gap-3 rounded-md border border-zinc-800 p-3 hover:bg-zinc-900/60">
          <input
            type="radio"
            bind:group={endpointMode}
            value="hosted"
            class="mt-0.5 accent-emerald-500"
          />
          <div class="flex-1">
            <div class="text-sm font-medium">hopssh.com</div>
            <div class="text-[11px] text-zinc-500">Hosted by us. The fastest way to start.</div>
          </div>
        </label>
        <label class="flex cursor-pointer items-start gap-3 rounded-md border border-zinc-800 p-3 hover:bg-zinc-900/60">
          <input
            type="radio"
            bind:group={endpointMode}
            value="selfhosted"
            class="mt-0.5 accent-emerald-500"
          />
          <div class="flex-1">
            <div class="text-sm font-medium">Self-hosted</div>
            <div class="text-[11px] text-zinc-500">Run your own server.</div>
            {#if endpointMode === 'selfhosted'}
              <input
                type="text"
                bind:value={selfHostedUrl}
                class="mt-2 w-full rounded-md border border-zinc-800 bg-zinc-950 px-2.5 py-1.5 text-xs focus:border-emerald-400 focus:outline-none"
                placeholder="https://hopssh.example.com"
                onclick={(e) => e.stopPropagation()}
              />
            {/if}
          </div>
        </label>
      </fieldset>

      {#if stage === 'error'}
        <div class="rounded-md border border-red-900/40 bg-red-950/40 px-3 py-2 text-xs text-red-300">
          {errorMessage}
        </div>
      {/if}

      <button
        type="submit"
        disabled={endpointMode === 'selfhosted' && !selfHostedUrl.trim()}
        class="inline-flex w-full items-center justify-center rounded-md bg-emerald-500 px-3 py-2 text-sm font-medium text-zinc-950 hover:bg-emerald-400 disabled:opacity-60"
      >
        Continue with browser
      </button>
    </form>
  {:else if stage === 'starting' || stage === 'pending' || stage === 'completing' || stage === 'bgprompt' || stage === 'bgconverting' || stage === 'done'}
    <!-- Progress ladder visible across the entire enrollment lifecycle.
         Replaces the previous spinner-only "waiting for approval" UX
         with a clear "you are at step N of 3". -->
    <ol class="mt-6 space-y-2">
      {#each steps as s, i}
        <li class="flex items-center gap-3 text-sm">
          <span
            class={
              s.done
                ? 'inline-flex h-6 w-6 items-center justify-center rounded-full bg-emerald-500 text-zinc-950 text-[11px] font-semibold'
                : s.active
                  ? 'inline-flex h-6 w-6 items-center justify-center rounded-full border-2 border-emerald-500 text-emerald-400 text-[11px] font-semibold'
                  : 'inline-flex h-6 w-6 items-center justify-center rounded-full border border-zinc-700 text-zinc-500 text-[11px]'
            }
          >
            {#if s.done}
              ✓
            {:else}
              {i + 1}
            {/if}
          </span>
          <span class={s.done || s.active ? 'text-zinc-100' : 'text-zinc-500'}>
            {s.label}
          </span>
          {#if s.active}
            <svg
              class="h-3.5 w-3.5 animate-spin text-emerald-400"
              viewBox="0 0 24 24"
              fill="none"
              stroke="currentColor"
              stroke-width="2"
              stroke-linecap="round"
            >
              <path d="M21 12a9 9 0 1 1-6.219-8.56" />
            </svg>
          {/if}
        </li>
      {/each}
    </ol>

    {#if stage === 'pending'}
      <p class="mt-6 text-sm text-zinc-300">
        We opened a browser tab to approve this Mac.
      </p>
      <button
        type="button"
        class="mt-2 text-xs text-emerald-400 underline hover:text-emerald-300"
        onclick={reopenBrowser}
      >
        Browser didn't open? Click here.
      </button>

      <details
        class="mt-6"
        ontoggle={(e) => (showCode = (e.currentTarget as HTMLDetailsElement).open)}
      >
        <summary class="cursor-pointer text-xs text-zinc-400 hover:text-zinc-200">
          Approving from another device?
        </summary>
        <div class="mt-3 rounded-md border border-zinc-800 bg-zinc-900 px-4 py-3">
          <div class="text-[11px] uppercase tracking-wide text-zinc-500">Enter this code</div>
          <div class="mt-1 flex items-center justify-between gap-2">
            <span class="font-mono text-2xl tracking-widest text-emerald-400">{userCode}</span>
            <div class="flex flex-col gap-1">
              <button
                class="rounded-md bg-zinc-800 px-2 py-1 text-[11px] hover:bg-zinc-700"
                onclick={copyCodeBare}
                type="button"
                title="Copy just the 4 characters"
              >
                Copy 4 chars
              </button>
              <button
                class="rounded-md border border-zinc-700 px-2 py-1 text-[11px] text-zinc-400 hover:text-zinc-200"
                onclick={copyCodeWithPrefix}
                type="button"
                title="Copy with HOP- prefix"
              >
                Copy with HOP-
              </button>
            </div>
          </div>
        </div>
        <p class="mt-2 text-[11px] text-zinc-500">
          Open <span class="font-mono text-zinc-400">{verificationUrl.replace(/\?.*$/, '')}</span>
          on any device that's signed in.
        </p>
      </details>

      <button
        class="mt-6 text-xs text-zinc-400 hover:text-zinc-200"
        onclick={cancel}
        type="button"
      >
        Cancel
      </button>
    {:else if stage === 'completing'}
      <p class="mt-6 text-sm text-zinc-300">Approved. Bringing the mesh up…</p>
    {:else if stage === 'bgprompt'}
      <!-- One-time post-enrollment prompt: Tailscale-style "Run in
           background" pitch, surfaced at the natural high-commitment
           moment (right after the user enrolled). Decision is recorded
           in localStorage so we don't nag again. -->
      <div class="mt-6 rounded-lg border border-emerald-900/40 bg-emerald-950/30 p-4">
        <div class="flex items-start gap-3">
          <span class="mt-0.5 text-xl">🔋</span>
          <div class="min-w-0 flex-1">
            <h3 class="text-sm font-semibold text-emerald-100">
              Enable system-wide networking?
            </h3>
            <p class="mt-1 text-[12px] leading-relaxed text-zinc-300">
              Recommended. Without this the mesh only works inside hopssh —
              <code class="rounded bg-zinc-900/60 px-1 font-mono">ping</code>,
              <code class="rounded bg-zinc-900/60 px-1 font-mono">ssh</code>,
              browsers, and other apps can't reach mesh IPs. Installing a
              small background service gives every app on your Mac access
              to mesh hostnames, keeps the connection alive when you quit
              hopssh, and reconnects after restart.
            </p>
            <p class="mt-1 text-[11px] text-zinc-500">
              Triggers a one-time admin prompt. Reversible anytime from
              Settings → Preferences.
            </p>
            {#if bgPromptError}
              <div class="mt-2 rounded-md border border-red-900/50 bg-red-950/40 px-3 py-2 text-[11px] text-red-300">
                {bgPromptError}
              </div>
            {/if}
            <div class="mt-3 flex gap-2">
              <button
                type="button"
                class="rounded-md bg-emerald-500 px-3 py-1.5 text-xs font-medium text-zinc-950 hover:bg-emerald-400"
                onclick={acceptBackground}
              >
                Allow
              </button>
              <button
                type="button"
                class="rounded-md border border-zinc-700 px-3 py-1.5 text-xs text-zinc-300 hover:bg-zinc-800"
                onclick={declineBackground}
              >
                Only while hopssh is open
              </button>
            </div>
          </div>
        </div>
      </div>
    {:else if stage === 'bgconverting'}
      <p class="mt-6 text-sm text-zinc-300">Setting up background mode…</p>
    {:else if stage === 'done'}
      <div class="mt-6 rounded-md border border-emerald-900/40 bg-emerald-950/40 px-4 py-3 text-sm text-emerald-300">
        ✓ Connected.
      </div>
    {/if}
  {/if}
</div>
