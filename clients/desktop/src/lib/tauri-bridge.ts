/**
 * Tiny shim around Tauri-only APIs so the same Svelte code runs in
 * vite-only browser dev mode (where window.open / clipboard fall back to
 * standard browser APIs) and in Tauri (where we route through our own
 * `open_external_url` Tauri command — that command falls back to direct
 * browser invocation on Linux when xdg-open is misconfigured, e.g.
 * Ubuntu + snap firefox without ~/.config/mimeapps.list).
 *
 * v0.11.23: switched away from tauri-plugin-opener.openUrl on Tauri to
 * our own open_external_url Tauri command. tauri-plugin-opener wraps
 * xdg-open which silently exits 0 on Linux when no default browser is
 * registered, leaving the user staring at "We opened a browser tab"
 * with nothing actually happening. Our command tries xdg-open first
 * (only when xdg-settings reports a default), then falls through to
 * direct browser invocation (firefox, chromium, etc.).
 */

export async function openExternal(url: string): Promise<void> {
  if ('__TAURI_INTERNALS__' in window) {
    const { invoke } = await import('@tauri-apps/api/core');
    await invoke('open_external_url', { url });
    return;
  }
  window.open(url, '_blank', 'noopener,noreferrer');
}
