/**
 * Tiny shim around Tauri-only APIs so the same Svelte code runs in
 * vite-only browser dev mode (where window.open / clipboard fall back to
 * standard browser APIs) and in Tauri (where we use the opener plugin to
 * route to the native default browser).
 */

export async function openExternal(url: string): Promise<void> {
  if ('__TAURI_INTERNALS__' in window) {
    const { openUrl } = await import('@tauri-apps/plugin-opener');
    await openUrl(url);
    return;
  }
  window.open(url, '_blank', 'noopener,noreferrer');
}
