/**
 * TypeScript client for the hop-agent local HTTP API (loopback only,
 * bearer-token auth). Mirrors cmd/agent/local_api.go endpoints exactly.
 *
 * The Tauri Rust shell scrapes the HOPSSH_LOCAL_API:<host:port>:<token>
 * line from the agent's stdout when it spawns it as a sidecar; the shell
 * exposes that endpoint+token to the WebView via Tauri commands. In dev
 * (vite-only mode without Tauri), set VITE_HOPSSH_API + VITE_HOPSSH_TOKEN
 * env vars to talk to a manually-launched agent.
 */

import { invoke } from '@tauri-apps/api/core';

let cached: { base: string; token: string } | null = null;

async function readEnvFromTauri(): Promise<{ base: string; token: string } | null> {
  // We treat the absence of __TAURI__ on window as "browser dev mode".
  if (!('__TAURI_INTERNALS__' in window)) return null;
  try {
    const ep = await invoke<{ host: string; token: string }>('local_api_endpoint');
    return { base: `http://${ep.host}`, token: ep.token };
  } catch (e) {
    console.error('local_api_endpoint Tauri command failed:', e);
    return null;
  }
}

function readEnvFromVite(): { base: string; token: string } | null {
  const base = (import.meta.env.VITE_HOPSSH_API as string | undefined)?.trim();
  const token = (import.meta.env.VITE_HOPSSH_TOKEN as string | undefined)?.trim();
  if (base && token) return { base, token };
  return null;
}

async function endpoint(): Promise<{ base: string; token: string }> {
  if (cached) return cached;
  const fromTauri = await readEnvFromTauri();
  if (fromTauri) {
    cached = fromTauri;
    return cached;
  }
  const fromVite = readEnvFromVite();
  if (fromVite) {
    cached = fromVite;
    return cached;
  }
  throw new Error(
    'No agent endpoint configured. Set VITE_HOPSSH_API + VITE_HOPSSH_TOKEN in .env.local for browser dev, or run via `npm run tauri:dev`.'
  );
}

/**
 * resetCachedEndpoint clears the module-level endpoint cache so the
 * next request() / subscribeEvents call re-runs the Tauri command
 * and picks up a freshly-published endpoint.
 *
 * Must be called whenever the underlying agent process changes — the
 * Settings → "Run in the background" toggle invokes
 * `convert_to_system_service` / `revert_to_bundled` which SIGKILL the
 * old hop-agent and bring up a new one at a different loopback port.
 * Without this, the JS layer keeps fetching the dead port and the UI
 * shows "agent unreachable" even though a healthy agent is running
 * on the new port.
 *
 * Wired in stores.svelte.ts via a Tauri event listener for
 * `agent-ready` (emitted by the Rust shell whenever it transitions
 * to a new endpoint).
 */
export function resetCachedEndpoint(): void {
  cached = null;
}

export class ApiError extends Error {
  constructor(public status: number, message: string) {
    super(message);
    this.name = 'ApiError';
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const { base, token } = await endpoint();
  const headers: Record<string, string> = {
    Authorization: `Bearer ${token}`
  };
  if (body !== undefined) headers['Content-Type'] = 'application/json';

  let res: Response;
  try {
    res = await fetch(`${base}${path}`, {
      method,
      headers,
      body: body !== undefined ? JSON.stringify(body) : undefined
    });
  } catch (e) {
    // Network-level fetch failure (TypeError "Failed to fetch") usually
    // means the cached endpoint points at a dead loopback port — e.g.
    // the bundled child got SIGKILLed by convert_to_system_service and
    // the agent-ready event is in flight but hasn't been processed yet.
    // Clear the cache so the next request() re-runs the Tauri command
    // and picks up the new endpoint. Caller-visible behavior is the
    // same (this throws), but the NEXT call self-heals.
    cached = null;
    throw e;
  }

  if (!res.ok) {
    let msg = res.statusText;
    try {
      const text = await res.text();
      try {
        msg = JSON.parse(text).error || text || msg;
      } catch {
        msg = text.trim() || msg;
      }
    } catch {
      // ignore
    }
    throw new ApiError(res.status, msg);
  }

  if (res.status === 204) return undefined as T;
  if (res.headers.get('Content-Type')?.includes('application/json')) {
    return (await res.json()) as T;
  }
  return undefined as T;
}

// --- Types (must match Go cmd/agent/local_api.go) ---

export interface EnrollmentStatus {
  name: string;
  endpoint: string;
  nodeId: string;
  dnsDomain?: string;
  tunMode?: string;
  listenPort?: number;
  meshIp?: string;
  nebulaIp?: string;
  certExpiresIn?: string;
  certNotAfter?: string;
  connected: boolean;
  peersDirect: number;
  peersRelayed: number;
  lastError?: string;
  // Phase L: per-(device, network) opt-in for clipboard sync. Off
  // by default; flipped via local.setClipboardSync(). Toggle takes
  // effect on the next agent restart in v1.
  clipboardSync?: boolean;
}

export interface ParallelInstall {
  launchDaemon: boolean;
  legacyConfigDir: boolean;
}

export interface LocalStatus {
  version: string;
  commit: string;
  os: string;
  arch: string;
  configDir: string;
  serviceStatus?: string;
  // runMode is "bundled" (.app spawns its own child) or "system"
  // (talks to a launchd LaunchDaemon agent). Drives the "Run in the
  // background" toggle in Settings.
  runMode?: 'bundled' | 'system';
  enrollments: EnrollmentStatus[];
  parallelInstall?: ParallelInstall;
}

export interface PeerDetail {
  vpnAddr: string;
  direct: boolean;
  remoteAddr?: string;
  rttMs?: number;
  // Human-readable identifiers populated from the server's heartbeat
  // peerInfo field. Optional (older agents/servers omit). Connected.svelte
  // shows `name` as the primary label and surfaces dnsHostname /
  // customDnsNames only in kernel-tun (system) mode where the OS
  // resolver can actually resolve them.
  name?: string;
  dnsHostname?: string;
  customDnsNames?: string[];
}

export interface PeersResponse {
  enrollment: string;
  connected: boolean;
  direct: number;
  relayed: number;
  peers: PeerDetail[];
}

export interface DeviceFlowStartResp {
  deviceCode: string;
  userCode: string;
  verificationUrl: string;
  expiresIn: number;
  interval: number;
}

export interface DeviceFlowPollResp {
  status: 'pending' | 'expired' | 'complete' | 'error';
  message?: string;
  enrollment?: string;
}

export interface LocalEvent {
  time: string;
  type: string;
  data: Record<string, unknown>;
}

// --- Endpoints ---

export const local = {
  health: () => request<{ ok: boolean; version: string; commit: string }>('GET', '/local/health'),
  status: () => request<LocalStatus>('GET', '/local/status'),
  peers: (enrollment?: string) =>
    request<PeersResponse>(
      'GET',
      `/local/peers${enrollment ? `?enrollment=${encodeURIComponent(enrollment)}` : ''}`
    ),
  enrollDeviceFlowStart: (opts: { endpoint?: string; name?: string; tunMode?: string }) =>
    request<DeviceFlowStartResp>('POST', '/local/enroll/device-flow/start', opts),
  enrollDeviceFlowPoll: (deviceCode: string) =>
    request<DeviceFlowPollResp>('POST', '/local/enroll/device-flow/poll', { deviceCode }),
  enrollToken: (opts: { token: string; endpoint?: string; name?: string; tunMode?: string }) =>
    request<{ enrollment: string }>('POST', '/local/enroll/token', opts),
  leave: (enrollmentName: string) =>
    request<{ removed: string; restartRequired: boolean }>(
      'POST',
      '/local/leave',
      { enrollment: enrollmentName }
    ),
  connect: (enrollmentName: string) =>
    request<{ status: string }>(
      'POST',
      `/local/connect?enrollment=${encodeURIComponent(enrollmentName)}`
    ),
  disconnect: (enrollmentName: string) =>
    request<void>(
      'POST',
      `/local/disconnect?enrollment=${encodeURIComponent(enrollmentName)}`
    ),
  setClipboardSync: (enrollmentName: string, enabled: boolean) =>
    request<{ enrollment: string; clipboardSync: boolean; restartRequired: boolean }>(
      'POST',
      '/local/clipboard-sync',
      { enrollment: enrollmentName, enabled }
    )
  // NOTE: a `local.forceRenew(name)` method existed briefly in
  // v0.10.80 (Phase P4) and was removed in v0.10.81. Renewal is fully
  // automatic — timer-driven loop + Phase P2 watchdog (auto-recover
  // from silent goroutine deaths) + Phase Q FS watcher (auto-re-attach
  // when daemon respawns). The agent-side POST /local/renew endpoint
  // stays for future debug/support tooling but has no JS surface in
  // the default UI.
};

/**
 * Subscribe to the agent's SSE event stream. Calls onEvent for every
 * frame. Returns a cancel function. Auto-reconnects on transport errors
 * with exponential backoff.
 */
export async function subscribeEvents(
  onEvent: (ev: LocalEvent) => void,
  onStatus?: (online: boolean) => void
): Promise<() => void> {
  let cancelled = false;
  let aborter: AbortController | null = null;
  let backoff = 500;

  async function loop() {
    while (!cancelled) {
      try {
        const { base, token } = await endpoint();
        aborter = new AbortController();
        const res = await fetch(`${base}/local/events`, {
          headers: { Authorization: `Bearer ${token}` },
          signal: aborter.signal
        });
        if (!res.ok || !res.body) {
          throw new Error(`SSE HTTP ${res.status}`);
        }
        onStatus?.(true);
        backoff = 500;

        const reader = res.body.getReader();
        const decoder = new TextDecoder();
        let buf = '';
        for (;;) {
          const { done, value } = await reader.read();
          if (done) break;
          buf += decoder.decode(value, { stream: true });
          // SSE frames are separated by \n\n.
          let idx;
          while ((idx = buf.indexOf('\n\n')) >= 0) {
            const frame = buf.slice(0, idx);
            buf = buf.slice(idx + 2);
            const line = frame.split('\n').find((l) => l.startsWith('data:'));
            if (!line) continue;
            try {
              const ev = JSON.parse(line.slice(5).trim()) as LocalEvent;
              onEvent(ev);
            } catch {
              // ignore malformed frame
            }
          }
        }
      } catch (e) {
        if (cancelled) return;
        // Same defensive cache-reset as request(): if the cached
        // endpoint is stale (e.g. the agent we were talking to just
        // got replaced by Run-in-the-background's convert), clear it
        // so the next loop iteration re-runs the Tauri command.
        cached = null;
        onStatus?.(false);
        await new Promise((r) => setTimeout(r, backoff));
        backoff = Math.min(backoff * 2, 10_000);
      }
    }
  }

  loop();

  return () => {
    cancelled = true;
    aborter?.abort();
  };
}
