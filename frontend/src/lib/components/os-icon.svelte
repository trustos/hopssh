<script lang="ts">
	// Phase II.4 (v0.11.4): per-OS icon used in node tables and peer
	// lists. Brand-mark inline SVGs (no new dep) wrapped in a shadcn
	// Tooltip showing the human-readable OS name on hover. Mirrors the
	// pattern used by Tailscale's device list — visual scannability +
	// hover affordance for any user unfamiliar with the marks.
	//
	// Used by frontend/src/routes/(app)/networks/[id]/+page.svelte's
	// node table OS column. The desktop client (Tauri/Svelte 5
	// without shadcn) rolls its own version with the native HTML
	// `title=` attribute as the tooltip — same SVGs, same data
	// surface, different tooltip primitive due to different dep tree.
	import * as Tooltip from '$lib/components/ui/tooltip';

	type Props = {
		os: string | null | undefined;
		// Forwarded as the icon's class so callers can scope size /
		// color in their own grid context. Default size matches the
		// dashboard's xs row text.
		class?: string;
	};

	let { os, class: cls = 'h-3.5 w-3.5 text-muted-foreground' }: Props = $props();

	function osLabel(o: string): string {
		switch (o) {
			case 'darwin':
				return 'macOS';
			case 'linux':
				return 'Linux';
			case 'windows':
				return 'Windows';
			default:
				return o;
		}
	}
</script>

{#if os === 'darwin'}
	<Tooltip.Provider delayDuration={150}>
		<Tooltip.Root>
			<Tooltip.Trigger>
				<svg class={cls} viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg" aria-label="macOS">
					<path
						d="M17.05 12.04c-.03-3.18 2.6-4.7 2.72-4.78-1.49-2.17-3.8-2.47-4.62-2.5-1.97-.2-3.84 1.16-4.84 1.16-1.01 0-2.55-1.13-4.19-1.1-2.16.03-4.15 1.25-5.26 3.18-2.24 3.88-.57 9.62 1.62 12.78 1.07 1.55 2.34 3.28 4.01 3.22 1.61-.07 2.22-1.04 4.17-1.04 1.94 0 2.49 1.04 4.2 1 1.74-.03 2.83-1.57 3.89-3.13 1.23-1.79 1.74-3.54 1.77-3.63-.04-.02-3.39-1.3-3.42-5.16zM14.13 3.3c.88-1.07 1.48-2.55 1.32-4.03-1.27.05-2.81.85-3.72 1.92-.81.95-1.53 2.46-1.34 3.91 1.42.11 2.85-.72 3.74-1.8z"
					/>
				</svg>
			</Tooltip.Trigger>
			<Tooltip.Content>macOS</Tooltip.Content>
		</Tooltip.Root>
	</Tooltip.Provider>
{:else if os === 'linux'}
	<Tooltip.Provider delayDuration={150}>
		<Tooltip.Root>
			<Tooltip.Trigger>
				<svg class={cls} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round" xmlns="http://www.w3.org/2000/svg" aria-label="Linux">
					<path d="M12 4c2.5 0 4 2.5 4 6 0 2.5-1 4.5-1 6 0 2 1.8 3 1.8 5H7.2c0-2 1.8-3 1.8-5 0-1.5-1-3.5-1-6 0-3.5 1.5-6 4-6z" />
					<circle cx="10.5" cy="9" r="0.6" fill="currentColor" />
					<circle cx="13.5" cy="9" r="0.6" fill="currentColor" />
				</svg>
			</Tooltip.Trigger>
			<Tooltip.Content>Linux</Tooltip.Content>
		</Tooltip.Root>
	</Tooltip.Provider>
{:else if os === 'windows'}
	<Tooltip.Provider delayDuration={150}>
		<Tooltip.Root>
			<Tooltip.Trigger>
				<svg class={cls} viewBox="0 0 24 24" fill="currentColor" xmlns="http://www.w3.org/2000/svg" aria-label="Windows">
					<path d="M3 5.5L10.5 4.5V11H3V5.5zM10.5 12V19L3 18V12H10.5zM11.5 4.4L21 3v9h-9.5V4.4zM21 13v8L11.5 19.7V13H21z" />
				</svg>
			</Tooltip.Trigger>
			<Tooltip.Content>Windows</Tooltip.Content>
		</Tooltip.Root>
	</Tooltip.Provider>
{:else if os}
	<!-- Unknown OS: surface the raw value as text so it doesn't
	     silently disappear behind a generic icon. Matches the
	     dashboard's prior fallback. -->
	<Tooltip.Provider delayDuration={150}>
		<Tooltip.Root>
			<Tooltip.Trigger>
				<span class="text-muted-foreground text-xs">{os}</span>
			</Tooltip.Trigger>
			<Tooltip.Content>{osLabel(os)}</Tooltip.Content>
		</Tooltip.Root>
	</Tooltip.Provider>
{:else}
	<span class="text-muted-foreground/50">—</span>
{/if}
