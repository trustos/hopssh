/**
 * Reactive Svelte 5 stores backed by the local API + SSE event stream.
 * All components read from these; only this module talks to local-api.ts.
 */

import { invoke } from '@tauri-apps/api/core';
import {
  local,
  subscribeEvents,
  type LocalStatus,
  type LocalEvent
} from './local-api';

const isTauri = typeof window !== 'undefined' && '__TAURI_INTERNALS__' in window;

function buildTrayTooltip(s: LocalStatus | null): string {
  if (!s) return 'hopssh — connecting…';
  if (s.enrollments.length === 0) return 'hopssh — no networks';
  const lines: string[] = [];
  for (const e of s.enrollments) {
    if (e.connected) {
      const path = e.peersDirect > 0 ? 'P2P' : e.peersRelayed > 0 ? 'relay' : '—';
      lines.push(`${e.name}: ${e.nebulaIp ?? '?'} (${path}, ${e.peersDirect + e.peersRelayed} peer${
        e.peersDirect + e.peersRelayed === 1 ? '' : 's'
      })`);
    } else {
      lines.push(`${e.name}: disconnected`);
    }
  }
  return `hopssh\n${lines.join('\n')}`;
}

async function syncTrayTooltip(status: LocalStatus | null) {
  if (!isTauri) return;
  try {
    await invoke('set_tray_tooltip', { tooltip: buildTrayTooltip(status) });
  } catch {
    // benign — tray may not yet be ready during very-early startup.
  }
}

class AgentStore {
  status = $state<LocalStatus | null>(null);
  online = $state(false);
  lastError = $state<string | null>(null);
  initialLoad = $state(true);

  events = $state<LocalEvent[]>([]);
  private cancelEvents: (() => void) | null = null;

  async refresh() {
    try {
      const s = await local.status();
      this.status = s;
      this.online = true;
      this.lastError = null;
      void syncTrayTooltip(s);
    } catch (e: unknown) {
      this.online = false;
      this.lastError = e instanceof Error ? e.message : String(e);
    } finally {
      this.initialLoad = false;
    }
  }

  async start() {
    await this.refresh();
    this.cancelEvents = await subscribeEvents(
      (ev) => {
        // Keep at most 200 events for the activity drawer.
        this.events = [ev, ...this.events].slice(0, 200);
        if (ev.type === 'status' && ev.data && typeof ev.data === 'object') {
          // SSE pushes a fresh status snapshot every 5s; merge it in.
          this.status = ev.data as unknown as LocalStatus;
          this.online = true;
          void syncTrayTooltip(this.status);
        }
        if (
          ev.type === 'enrollment.added' ||
          ev.type === 'enrollment.removed'
        ) {
          // Force a fresh fetch for canonical state.
          this.refresh();
        }
      },
      (online) => {
        this.online = online;
        if (!online) this.lastError = 'agent unreachable';
      }
    );
  }

  stop() {
    this.cancelEvents?.();
    this.cancelEvents = null;
  }
}

export const agent = new AgentStore();
