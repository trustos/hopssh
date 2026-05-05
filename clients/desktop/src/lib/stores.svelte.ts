/**
 * Reactive Svelte 5 stores backed by the local API + SSE event stream.
 * All components read from these; only this module talks to local-api.ts.
 */

import { invoke } from '@tauri-apps/api/core';
import { listen, type UnlistenFn } from '@tauri-apps/api/event';
import {
  local,
  resetCachedEndpoint,
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
  // firstFailureAt is the epoch-ms of the first connection failure
  // since the last successful connect. Reset to null on success.
  // The UI uses this to distinguish "transient retry blip" (< ~10s)
  // from "actually broken" — only the latter shows the hard error
  // screen. Anything below the threshold renders a friendly
  // "Connecting…" spinner while SSE backoff retries silently.
  firstFailureAt = $state<number | null>(null);

  events = $state<LocalEvent[]>([]);
  banners = $state<Banner[]>([]);
  private nextBannerId = 1;
  private cancelEvents: (() => void) | null = null;
  private cancelAgentReady: UnlistenFn | null = null;

  // subscribeFn is the same SSE-subscribe call we make from start().
  // Extracted into a method so the agent-ready handler can re-subscribe
  // after invalidating the endpoint cache without duplicating the
  // (event, onStatus) closures.
  private async subscribeSSE() {
    return await subscribeEvents(
      (ev) => {
        // Keep at most 200 events for the activity drawer.
        this.events = [ev, ...this.events].slice(0, 200);
        if (ev.type === 'status' && ev.data && typeof ev.data === 'object') {
          this.status = ev.data as unknown as LocalStatus;
          this.online = true;
          this.firstFailureAt = null;
          void syncTrayTooltip(this.status);
          void syncTrayState(this.status);
          // State-based clear of stale watchdog warnings: if any
          // enrollment is connected with at least one direct OR
          // relayed peer, the "data-plane stuck — recovering" banner
          // for that enrollment is contradicted by current reality
          // and should clear. Belts-and-braces with the dismissAt TTL.
          this.clearStaleWarningsForHealthyState(this.status);
        }
        if (
          ev.type === 'enrollment.added' ||
          ev.type === 'enrollment.removed'
        ) {
          this.refresh();
        }
        this.handleBannerEvent(ev);
      },
      (online) => {
        this.online = online;
        if (online) {
          this.firstFailureAt = null;
        } else {
          this.lastError = 'agent unreachable';
          if (this.firstFailureAt === null) this.firstFailureAt = Date.now();
        }
      }
    );
  }

  async refresh() {
    try {
      const s = await local.status();
      this.status = s;
      this.online = true;
      this.lastError = null;
      this.firstFailureAt = null;
      void syncTrayTooltip(s);
      void syncTrayState(s);
    } catch (e: unknown) {
      this.online = false;
      this.lastError = e instanceof Error ? e.message : String(e);
      if (this.firstFailureAt === null) this.firstFailureAt = Date.now();
    } finally {
      this.initialLoad = false;
    }
  }

  async start() {
    await this.refresh();
    this.cancelEvents = await this.subscribeSSE();

    // Listen for the Tauri-side `agent-ready` event. Emitted by the
    // Rust shell whenever the underlying agent endpoint changes —
    // currently from convert_to_system_service / revert_to_bundled
    // (Settings → Run in the background toggle), but the contract
    // generalizes to any future runtime mode swap.
    //
    // Without this, the JS layer's module-level endpoint cache stays
    // pinned to the old (now-dead) loopback port and the UI shows
    // "agent unreachable" indefinitely. See Phase D in the plan.
    if (isTauri) {
      this.cancelAgentReady = await listen('agent-ready', async () => {
        // Tear down the SSE stream — its outer-loop fetch is holding
        // the old endpoint.
        this.cancelEvents?.();
        this.cancelEvents = null;
        // Clear the module-level cache so the next request() re-runs
        // the local_api_endpoint Tauri command.
        resetCachedEndpoint();
        // Re-fetch status against the new endpoint + re-subscribe.
        await this.refresh();
        this.cancelEvents = await this.subscribeSSE();
      });
    }

    // Periodic banner GC: expire stale banners every 1s.
    this.scheduleBannerSweep();
  }

  stop() {
    this.cancelEvents?.();
    this.cancelEvents = null;
    this.cancelAgentReady?.();
    this.cancelAgentReady = null;
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
        // Phase BB (v0.10.93): plain-English copy. "data-plane" was
        // internal jargon. The user just needs to know hopssh is
        // recovering from a transient connection issue.
        const cycles = (ev.data?.consecutiveStuck as number | undefined) ?? 0;
        this.pushBanner({
          kind: 'warning',
          title: enrollment
            ? `${enrollment}: connection issue — recovering automatically`
            : 'Connection issue — recovering automatically',
          detail: cycles > 0 ? 'Recovering automatically. This usually takes a few seconds.' : undefined,
          enrollment,
          // 10-minute TTL as a backstop. Normally cleared sooner by a
          // paired watchdog-recovered/failed event; this guards against
          // the case where the recovered event was emitted before the
          // .app's SSE was subscribed (rare but happens during rapid
          // dev-deploy cycles), or when restartFn is nil so the agent
          // never emits a follow-up. The .scheduleBannerSweep() GC
          // tick will expire it.
          dismissAt: Date.now() + 10 * 60 * 1000
        });
        break;
      }
      case 'instance.watchdog-recovered': {
        // Replace the matching tripped banner with a transient
        // "recovered" toast.
        // Phase BB (v0.10.93): "mesh recovered" → "connection restored"
        // — plain English, matches the watchdog-tripped wording.
        this.banners = this.banners.filter((b) => b.kind !== 'warning' || b.enrollment !== enrollment);
        this.pushBanner({
          kind: 'info',
          title: enrollment
            ? `${enrollment}: connection restored`
            : 'Connection restored',
          enrollment,
          dismissAt: Date.now() + 8000 // 8s auto-dismiss
        });
        break;
      }
      case 'instance.watchdog-recovery-failed': {
        // Phase BB (v0.10.93): "auto-recovery failed" → "couldn't
        // recover automatically" — friendlier phrasing for an error
        // banner the user can't directly fix.
        this.banners = this.banners.filter((b) => b.kind !== 'warning' || b.enrollment !== enrollment);
        this.pushBanner({
          kind: 'error',
          title: enrollment
            ? `${enrollment}: couldn't recover automatically`
            : "Couldn't recover automatically",
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

  // Clear watchdog warning banners for any enrollment whose live status
  // shows it's connected with at least one peer — that's the visible
  // proof that recovery succeeded, regardless of whether we received
  // the paired watchdog-recovered event.
  private clearStaleWarningsForHealthyState(s: LocalStatus | null) {
    if (!s || s.enrollments.length === 0) return;
    const healthy = new Set(
      s.enrollments
        .filter((e) => e.connected && (e.peersDirect > 0 || e.peersRelayed > 0))
        .map((e) => e.name)
    );
    if (healthy.size === 0) return;
    const next = this.banners.filter(
      (b) => !(b.kind === 'warning' && b.enrollment && healthy.has(b.enrollment))
    );
    if (next.length !== this.banners.length) this.banners = next;
  }

  dismissBanner(id: number) {
    this.banners = this.banners.filter((b) => b.id !== id);
  }
}

export const agent = new AgentStore();
