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

function aggregateTrayState(s: LocalStatus | null): 'connected' | 'relay' | 'disconnected' {
  if (!s || s.enrollments.length === 0) return 'disconnected';
  let anyDirect = false;
  let anyRelay = false;
  for (const e of s.enrollments) {
    if (!e.connected) continue;
    if (e.peersDirect > 0) anyDirect = true;
    if (e.peersRelayed > 0) anyRelay = true;
  }
  if (anyDirect) return 'connected';
  if (anyRelay) return 'relay';
  return 'disconnected';
}

async function syncTrayState(status: LocalStatus | null) {
  if (!isTauri) return;
  try {
    await invoke('set_tray_state', { state: aggregateTrayState(status) });
  } catch {
    // benign on early startup.
  }
}

// Banner is a transient message the UI surfaces in response to agent
// events that the user benefits from knowing about — e.g. the v0.10.36
// watchdog tripping or recovering. Banners auto-dismiss after a short
// hold time; the user can dismiss them manually.
export interface Banner {
  id: number;
  kind: 'info' | 'warning' | 'error';
  title: string;
  detail?: string;
  enrollment?: string;
  dismissAt: number; // epoch ms; 0 = sticky until user dismisses
}

class AgentStore {
  status = $state<LocalStatus | null>(null);
  online = $state(false);
  lastError = $state<string | null>(null);
  initialLoad = $state(true);

  events = $state<LocalEvent[]>([]);
  banners = $state<Banner[]>([]);
  private nextBannerId = 1;
  private cancelEvents: (() => void) | null = null;

  async refresh() {
    try {
      const s = await local.status();
      this.status = s;
      this.online = true;
      this.lastError = null;
      void syncTrayTooltip(s);
      void syncTrayState(s);
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
          void syncTrayState(this.status);
        }
        if (
          ev.type === 'enrollment.added' ||
          ev.type === 'enrollment.removed'
        ) {
          // Force a fresh fetch for canonical state.
          this.refresh();
        }
        this.handleBannerEvent(ev);
      },
      (online) => {
        this.online = online;
        if (!online) this.lastError = 'agent unreachable';
      }
    );
    // Periodic banner GC: expire stale banners every 1s.
    this.scheduleBannerSweep();
  }

  private bannerTimer: number | null = null;
  private scheduleBannerSweep() {
    if (this.bannerTimer !== null) return;
    this.bannerTimer = window.setInterval(() => {
      const now = Date.now();
      const next = this.banners.filter((b) => b.dismissAt === 0 || b.dismissAt > now);
      if (next.length !== this.banners.length) this.banners = next;
    }, 1000);
  }

  private handleBannerEvent(ev: LocalEvent) {
    const enrollment = (ev.data?.name as string | undefined) ?? undefined;
    switch (ev.type) {
      case 'instance.watchdog-tripped': {
        const cycles = (ev.data?.consecutiveStuck as number | undefined) ?? 0;
        this.pushBanner({
          kind: 'warning',
          title: enrollment
            ? `${enrollment}: data-plane stuck — recovering`
            : 'mesh data-plane stuck — recovering',
          detail: cycles > 0 ? `Detected after ${cycles} consecutive stuck cycles. Auto-restart in progress.` : undefined,
          enrollment,
          dismissAt: 0 // sticky until paired with recovered/failed
        });
        break;
      }
      case 'instance.watchdog-recovered': {
        // Replace the matching tripped banner with a transient
        // "recovered" toast.
        this.banners = this.banners.filter((b) => b.kind !== 'warning' || b.enrollment !== enrollment);
        this.pushBanner({
          kind: 'info',
          title: enrollment
            ? `${enrollment}: mesh recovered`
            : 'mesh recovered',
          enrollment,
          dismissAt: Date.now() + 8000 // 8s auto-dismiss
        });
        break;
      }
      case 'instance.watchdog-recovery-failed': {
        this.banners = this.banners.filter((b) => b.kind !== 'warning' || b.enrollment !== enrollment);
        this.pushBanner({
          kind: 'error',
          title: enrollment
            ? `${enrollment}: auto-recovery failed`
            : 'auto-recovery failed',
          detail: (ev.data?.error as string | undefined) ?? 'See agent logs for details.',
          enrollment,
          dismissAt: 0 // sticky — needs operator
        });
        break;
      }
      case 'instance.watchdog-cooldown': {
        // Don't surface; logged on agent side. This is an internal
        // signal that the cooldown suppressed an action — not
        // user-actionable.
        break;
      }
    }
  }

  private pushBanner(b: Omit<Banner, 'id'>) {
    const id = this.nextBannerId++;
    this.banners = [...this.banners, { id, ...b }];
  }

  dismissBanner(id: number) {
    this.banners = this.banners.filter((b) => b.id !== id);
  }

  stop() {
    this.cancelEvents?.();
    this.cancelEvents = null;
  }
}

export const agent = new AgentStore();
