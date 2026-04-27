import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';

const host = process.env.TAURI_DEV_HOST;

export default defineConfig({
  plugins: [svelte(), tailwindcss()],
  // Tauri requires a specific port; ignore unfound deps so we don't choke
  // on platform-conditional imports.
  clearScreen: false,
  server: {
    port: 1420,
    strictPort: true,
    host: host || false,
    hmr: host
      ? { protocol: 'ws', host, port: 1421 }
      : undefined,
    watch: {
      ignored: ['**/src-tauri/**']
    }
  },
  // Tauri's webview ships a custom protocol; build for ESnext/no-IE.
  build: {
    target: 'esnext',
    minify: 'esbuild',
    sourcemap: true
  }
});
