<script lang="ts">
	import * as Card from '$lib/components/ui/card/index.js';
	import { Button } from '$lib/components/ui/button/index.js';
	import * as Alert from '$lib/components/ui/alert/index.js';
	import * as Tabs from '$lib/components/ui/tabs/index.js';

	const installCmd = 'curl -fsSL https://hopssh.com/install-mac.sh | bash';
	const xattrCmd = 'sudo xattr -cr /Applications/hopssh.app';

	let copied = $state<'install' | 'xattr' | null>(null);
	function copy(text: string, which: 'install' | 'xattr') {
		void navigator.clipboard.writeText(text);
		copied = which;
		setTimeout(() => (copied = null), 1500);
	}
</script>

<svelte:head>
	<title>Get the Desktop App - hopssh</title>
</svelte:head>

<div class="mx-auto max-w-2xl space-y-6 p-6">
	<div>
		<h1 class="text-2xl font-bold">Get hopssh for macOS</h1>
		<p class="mt-1 text-sm text-muted-foreground">
			Tray app + native window for managing your mesh. Works alongside the
			CLI agent and any servers you've already enrolled.
		</p>
	</div>

	<Alert.Root>
		<Alert.Title>About the install warning</Alert.Title>
		<Alert.Description>
			hopssh isn't yet signed with an Apple Developer ID, so a Safari-downloaded
			DMG triggers macOS's <em>"Apple could not verify…"</em> dialog. The
			recommended one-line install below uses <code class="font-mono text-xs">curl</code>,
			which doesn't attach the <code class="font-mono text-xs"
				>com.apple.quarantine</code
			> tag that browsers do — so Gatekeeper has nothing to flag and the install runs
			cleanly. Apple Developer signing is on the roadmap.
		</Alert.Description>
	</Alert.Root>

	<Tabs.Root value="recommended" class="w-full">
		<Tabs.List class="grid w-full grid-cols-2">
			<Tabs.Trigger value="recommended">Recommended (one command)</Tabs.Trigger>
			<Tabs.Trigger value="manual">Manual (download DMG)</Tabs.Trigger>
		</Tabs.List>

		<Tabs.Content value="recommended">
			<Card.Root>
				<Card.Header>
					<Card.Title>One-line install</Card.Title>
					<Card.Description>
						Paste this into Terminal. Installs into <code class="font-mono text-xs"
							>/Applications</code
						> and launches automatically. Requires your admin password.
					</Card.Description>
				</Card.Header>
				<Card.Content class="space-y-3">
					<div class="rounded-md border bg-muted/30 p-3">
						<code class="block break-all font-mono text-[12px]">{installCmd}</code>
					</div>
					<Button variant="outline" size="sm" onclick={() => copy(installCmd, 'install')}>
						{copied === 'install' ? 'Copied!' : 'Copy command'}
					</Button>
					<div class="space-y-1.5 text-xs text-muted-foreground">
						<p class="font-medium text-foreground">What it does:</p>
						<ol class="list-decimal space-y-1 pl-5">
							<li>Detects your Mac's CPU architecture (Apple Silicon / Intel).</li>
							<li>Downloads the matching DMG with <code class="font-mono">curl</code>
								— so it never gets the quarantine tag.</li>
							<li>Mounts the DMG, copies <code class="font-mono">hopssh.app</code> into
								<code class="font-mono">/Applications</code>
								via <code class="font-mono">ditto --noextattr</code> (strips any
								xattrs that might've snuck in), ejects the DMG.</li>
							<li>Opens the app. The tray icon appears in your menubar.</li>
						</ol>
					</div>
				</Card.Content>
			</Card.Root>
		</Tabs.Content>

		<Tabs.Content value="manual">
			<Card.Root>
				<Card.Header>
					<Card.Title>Download the DMG</Card.Title>
					<Card.Description>
						If you'd rather click than paste. You'll need to unblock Gatekeeper once.
					</Card.Description>
				</Card.Header>
				<Card.Content class="space-y-4">
					<div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
						<Button variant="default" href="/download/desktop/hopssh-macos-aarch64.dmg">
							Download for Apple Silicon
						</Button>
						<Button variant="outline" href="/download/desktop/hopssh-macos-x86_64.dmg">
							Download for Intel
						</Button>
					</div>

					<div class="space-y-3 rounded-md border bg-muted/20 p-4 text-xs">
						<p class="font-medium text-foreground">After downloading:</p>
						<ol class="list-decimal space-y-2 pl-5 leading-relaxed text-muted-foreground">
							<li>Double-click the DMG to mount it.</li>
							<li>Drag <code class="font-mono">hopssh.app</code> into <code class="font-mono">Applications</code>.</li>
							<li>Eject the DMG.</li>
							<li>
								Open <code class="font-mono">Applications</code> in Finder and double-click
								<code class="font-mono">hopssh</code>. macOS shows
								<em>"Apple could not verify…"</em> and refuses to open it. <strong>This
									is expected.</strong>
								Use one of the unblock paths below — once is enough.
							</li>
						</ol>
					</div>

					<div class="space-y-3 rounded-md border border-amber-500/20 bg-amber-500/5 p-4 text-xs">
						<p class="font-medium text-foreground">Unblock Gatekeeper (pick one)</p>

						<div>
							<p class="font-medium text-foreground">A. Terminal (one command, fastest):</p>
							<div class="mt-1 rounded-md border bg-background p-2">
								<code class="block break-all font-mono text-[11px]">{xattrCmd}</code>
							</div>
							<Button
								variant="outline"
								size="sm"
								class="mt-2"
								onclick={() => copy(xattrCmd, 'xattr')}
							>
								{copied === 'xattr' ? 'Copied!' : 'Copy command'}
							</Button>
							<p class="mt-1.5 text-muted-foreground">
								Strips the <code class="font-mono">com.apple.quarantine</code>
								tag Safari attached. Re-launch the app — no warning.
							</p>
						</div>

						<div>
							<p class="font-medium text-foreground">B. System Settings (UI):</p>
							<ol class="mt-1 list-decimal space-y-1 pl-5 text-muted-foreground">
								<li>Try to open <code class="font-mono">hopssh.app</code> once
									(the warning dialog appears — dismiss it).</li>
								<li>Open <strong>System Settings → Privacy &amp; Security</strong>.</li>
								<li>Scroll to the <strong>Security</strong> section. There should
									be a row saying
									<em>"hopssh was blocked from use because it is not from an
									identified developer."</em></li>
								<li>Click <strong>Open Anyway</strong> and confirm with your
									admin password.</li>
								<li>The app launches; macOS remembers the override for future
									runs.</li>
							</ol>
						</div>

						<div>
							<p class="font-medium text-foreground">C. Right-click → Open (older macOS only):</p>
							<p class="mt-1 text-muted-foreground">
								In Finder, right-click <code class="font-mono">hopssh.app</code>
								→ <strong>Open</strong> → confirm. <span class="text-amber-700 dark:text-amber-400"
									>This path is removed in macOS Sequoia (15.0) and later</span
								> — use option A or B on those versions.
							</p>
						</div>
					</div>
				</Card.Content>
			</Card.Root>
		</Tabs.Content>
	</Tabs.Root>

	<div class="text-center">
		<a href="/" class="text-xs text-muted-foreground hover:text-foreground hover:underline">
			← Back to dashboard
		</a>
	</div>
</div>
