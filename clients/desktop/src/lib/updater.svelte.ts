// Tauri auto-update glue.
//
// Two paths coexist:
//   1. Tauri plugin-updater (signed-update path) — requires a real pubkey
//      in tauri.conf.json. Fires once on app start; swallows errors.
//   2. Manual check via the control plane's /version endpoint — works
//      without signing infrastructure. User-driven from Settings.
//
// Until the signing keypair lands, path 2 is the load-bearing one. The
// install step opens the hopssh.com install page so the user runs the
// curl-pipe installer with full transparency about what's about to run.
//
// In dev/non-Tauri mode both paths no-op.

import { agent } from './stores.svelte';

const isTauri = typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window;
const VERSION_ENDPOINT = 'https://hopssh.com/version';

let pending: { version: string; install: () => Promise<void> } | null = null;

// Reactive state for the manual-check UI in Settings.
export const updateState = $state<{
  checking: boolean;
  current: string | null;     // .app's version, fetched once
  latest: string | null;      // server's GitHub-latest-release tag
  error: string | null;
  lastCheckedAt: number | null;
}>({
  checking: false,
  current: null,
  latest: null,
  error: null,
  lastCheckedAt: null,
});

/** Compare semver-like tags. Returns true if `latest` > `current`. */
export function isNewer(current: string | null, latest: string | null): boolean {
  if (!current || !latest) return false;
  const norm = (s: string) => s.replace(/^v/, '').split(/[.-]/).map((p) => Number(p) || 0);
  const a = norm(current);
  const b = norm(latest);
  for (let i = 0; i < Math.max(a.length, b.length); i++) {
    const av = a[i] ?? 0;
    const bv = b[i] ?? 0;
    if (bv > av) return true;
    if (bv < av) return false;
  }
  return false;
}

/** Read the .app's bundled version from Tauri. Returns null in dev. */
export async function appVersion(): Promise<string | null> {
  if (!isTauri) return null;
  try {
    const { getVersion } = await import('@tauri-apps/api/app');
    return await getVersion();
  } catch {
    return null;
  }
}

/** User-triggered check from Settings. Updates `updateState` reactively. */
export async function manualCheck(): Promise<void> {
  updateState.checking = true;
  updateState.error = null;
  try {
    if (updateState.current === null) {
      updateState.current = await appVersion();
    }
    const r = await fetch(VERSION_ENDPOINT, { cache: 'no-store' });
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
    const json = (await r.json()) as { version?: string };
    updateState.latest = json.version ?? null;
    updateState.lastCheckedAt = Date.now();
  } catch (e: unknown) {
    updateState.error = e instanceof Error ? e.message : String(e);
  } finally {
    updateState.checking = false;
  }
}

/** Open hopssh.com install instructions in the user's default browser. */
export async function openInstallPage(): Promise<void> {
  if (!isTauri) {
    window.open('https://hopssh.com/', '_blank');
    return;
  }
  const { openUrl } = await import('@tauri-apps/plugin-opener');
  await openUrl('https://hopssh.com/');
}

export async function checkForUpdate(): Promise<void> {
  if (!isTauri) return;
  try {
    const mod = await import('@tauri-apps/plugin-updater');
    const update = await mod.check();
    if (!update) return;
    pending = {
      version: update.version,
      install: async () => {
        await update.downloadAndInstall();
        // Tauri restarts the app on success; if it returns, surface
        // a banner to prompt manual relaunch.
        const { relaunch } = await import('@tauri-apps/plugin-process').catch(() => ({ relaunch: null }));
        if (relaunch) await relaunch();
      }
    };
    agent.banners = [
      ...agent.banners,
      {
        id: -1, // negative IDs reserved for the updater banner so it stays unique
        kind: 'info',
        title: `Update available: v${update.version}`,
        detail: 'Click to download and restart. Install at your convenience.',
        dismissAt: 0
      }
    ];
  } catch (e) {
    console.warn('updater check failed:', e);
  }
}

export async function installPendingUpdate(): Promise<void> {
  if (!pending) return;
  await pending.install();
}

export function pendingUpdateVersion(): string | null {
  return pending?.version ?? null;
}
