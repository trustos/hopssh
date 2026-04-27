// Tauri auto-update glue.
//
// On app start, check for an available update. If one's found, push
// a sticky banner; on click of "Install + restart", download +
// install. The user controls when the install fires; we never
// install automatically without their consent.
//
// In dev/non-Tauri mode this is a no-op (the dynamic import lives
// inside the isTauri check so the bundle has no Tauri-only imports
// at the top level).

import { agent } from './stores.svelte';

const isTauri = typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window;

let pending: { version: string; install: () => Promise<void> } | null = null;

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
