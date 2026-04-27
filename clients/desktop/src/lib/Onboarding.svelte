<script lang="ts">
  import { local } from './local-api';
  import { agent } from './stores.svelte';
  import { openExternal } from './tauri-bridge';

  let { onDone }: { onDone: () => void } = $props();

  let endpoint = $state('https://hopssh.com');
  let mode = $state<'idle' | 'starting' | 'pending' | 'error' | 'done'>('idle');
  let userCode = $state('');
  let verificationUrl = $state('');
  let pollDeadline = $state<number | null>(null);
  let errorMessage = $state('');
  let pollHandle: number | null = null;

  async function startFlow() {
    if (!endpoint.match(/^https?:\/\//)) {
      errorMessage = 'Endpoint must start with http:// or https://';
      mode = 'error';
      return;
    }
    mode = 'starting';
    errorMessage = '';
    try {
      const r = await local.enrollDeviceFlowStart({ endpoint });
      userCode = r.userCode;
      verificationUrl = r.verificationUrl;
      pollDeadline = Date.now() + r.expiresIn * 1000;
      mode = 'pending';
      // Open browser.
      void openExternal(verificationUrl);
      poll(r.deviceCode, r.interval * 1000);
    } catch (e: unknown) {
      errorMessage = e instanceof Error ? e.message : String(e);
      mode = 'error';
    }
  }

  async function poll(deviceCode: string, intervalMs: number) {
    const tick = async () => {
      if (mode !== 'pending') return;
      try {
        const r = await local.enrollDeviceFlowPoll(deviceCode);
        if (r.status === 'pending') {
          pollHandle = window.setTimeout(tick, intervalMs);
          return;
        }
        if (r.status === 'expired') {
          mode = 'error';
          errorMessage = 'Code expired. Try again.';
          return;
        }
        if (r.status === 'error') {
          mode = 'error';
          errorMessage = r.message ?? 'enrollment error';
          return;
        }
        if (r.status === 'complete') {
          mode = 'done';
          await agent.refresh();
          window.setTimeout(onDone, 1500);
          return;
        }
      } catch (e: unknown) {
        // transient — keep polling unless we've passed the deadline
        if (pollDeadline && Date.now() > pollDeadline) {
          mode = 'error';
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
    mode = 'idle';
    userCode = '';
    verificationUrl = '';
    pollDeadline = null;
    errorMessage = '';
  }

  function copyCode() {
    void navigator.clipboard.writeText(userCode);
  }
</script>

<div class="mx-auto max-w-md p-6">
  <h2 class="text-base font-semibold">Add a network</h2>
  <p class="mt-1 text-sm text-zinc-400">
    Connect this Mac to a hopssh control plane. You can use the hosted
    service at hopssh.com or your own self-hosted instance.
  </p>

  {#if mode === 'idle' || mode === 'starting' || mode === 'error'}
    <form
      class="mt-6 space-y-4"
      onsubmit={(e) => {
        e.preventDefault();
        startFlow();
      }}
    >
      <label class="block">
        <span class="text-xs uppercase tracking-wide text-zinc-400">Control plane URL</span>
        <input
          type="text"
          bind:value={endpoint}
          disabled={mode === 'starting'}
          class="mt-1 w-full rounded-md border border-zinc-800 bg-zinc-900 px-3 py-2 text-sm focus:border-emerald-400 focus:outline-none"
          placeholder="https://hopssh.com"
        />
      </label>

      {#if mode === 'error'}
        <div class="rounded-md border border-red-900/40 bg-red-950/40 px-3 py-2 text-xs text-red-300">
          {errorMessage}
        </div>
      {/if}

      <button
        type="submit"
        disabled={mode === 'starting'}
        class="inline-flex w-full items-center justify-center rounded-md bg-emerald-500 px-3 py-2 text-sm font-medium text-zinc-950 hover:bg-emerald-400 disabled:opacity-60"
      >
        {mode === 'starting' ? 'Requesting code…' : 'Continue with browser'}
      </button>
    </form>
  {:else if mode === 'pending'}
    <div class="mt-6 space-y-4">
      <p class="text-sm text-zinc-300">
        A browser window should have opened. If not, open
        <a class="text-emerald-400 underline" href={verificationUrl} target="_blank" rel="noreferrer">{verificationUrl}</a>
        manually.
      </p>
      <div class="rounded-md border border-zinc-800 bg-zinc-900 px-4 py-3">
        <div class="text-xs uppercase tracking-wide text-zinc-500">Enter this code</div>
        <div class="mt-1 flex items-center justify-between">
          <span class="font-mono text-2xl tracking-widest text-emerald-400">{userCode}</span>
          <button
            class="rounded-md bg-zinc-800 px-2 py-1 text-xs hover:bg-zinc-700"
            onclick={copyCode}
            type="button"
          >
            Copy
          </button>
        </div>
      </div>
      <p class="text-xs text-zinc-500">Waiting for approval…</p>
      <button
        class="text-xs text-zinc-400 hover:text-zinc-200"
        onclick={cancel}
        type="button"
      >
        Cancel
      </button>
    </div>
  {:else if mode === 'done'}
    <div class="mt-6 rounded-md border border-emerald-900/40 bg-emerald-950/40 px-4 py-3 text-sm text-emerald-300">
      Enrollment complete. Bringing the mesh up…
    </div>
  {/if}
</div>
