<script lang="ts">
	import * as Card from '$lib/components/ui/card/index.js';
	import { Button } from '$lib/components/ui/button/index.js';
	import * as Alert from '$lib/components/ui/alert/index.js';
	import * as Tabs from '$lib/components/ui/tabs/index.js';

	const macInstallCmd = 'curl -fsSL https://hopssh.com/install-mac.sh | bash';
	const macXattrCmd = 'sudo xattr -cr /Applications/hopssh.app';

	let copied = $state<'mac-install' | 'mac-xattr' | null>(null);
	function copy(text: string, which: 'mac-install' | 'mac-xattr') {
		void navigator.clipboard.writeText(text);
		copied = which;
		setTimeout(() => (copied = null), 1500);
	}

	// Default the visible platform tab to the user's OS so the page lands on
	// the relevant download without a click. Heuristic only — users can
	// switch tabs to grab a build for a different machine.
	function detectPlatform(): 'macos' | 'windows' | 'linux' {
		if (typeof navigator === 'undefined') return 'macos';
		const ua = navigator.userAgent.toLowerCase();
		if (ua.includes('win')) return 'windows';
		if (ua.includes('mac')) return 'macos';
		if (ua.includes('linux')) return 'linux';
		return 'macos';
	}
	const initialPlatform = detectPlatform();

	// Detect aarch64 (ARM64) Linux so we recommend the right AppImage.
	// navigator.userAgentData.platform is the modern accessor (Chrome 90+);
	// userAgent.includes('aarch64') / 'arm64' is the fallback for Firefox
	// and anything without the new API. Default to x86_64 if uncertain —
	// it's the more common case on desktop Linux.
	function detectLinuxArch(): 'x86_64' | 'aarch64' {
		if (typeof navigator === 'undefined') return 'x86_64';
		const ua = navigator.userAgent.toLowerCase();
		if (ua.includes('aarch64') || ua.includes('arm64')) return 'aarch64';
		return 'x86_64';
	}
	let linuxArch = $state(detectLinuxArch());
</script>

<svelte:head>
	<title>Get the Desktop App - hopssh</title>
</svelte:head>

<div class="mx-auto max-w-2xl space-y-6 p-6">
	<div>
		<h1 class="text-2xl font-bold">Get hopssh for your desktop</h1>
		<p class="mt-1 text-sm text-muted-foreground">
			Tray app + native window for managing your mesh. Works alongside the
			CLI agent and any servers you've already enrolled.
		</p>
	</div>

	<Tabs.Root value={initialPlatform} class="w-full">
		<Tabs.List class="grid w-full grid-cols-3">
			<Tabs.Trigger value="macos">macOS</Tabs.Trigger>
			<Tabs.Trigger value="windows">Windows</Tabs.Trigger>
			<Tabs.Trigger value="linux">Linux</Tabs.Trigger>
		</Tabs.List>

		<!-- ============= macOS ============= -->
		<Tabs.Content value="macos">
			<Alert.Root class="mt-4">
				<Alert.Title>About the install warning</Alert.Title>
				<Alert.Description>
					hopssh isn't yet signed with an Apple Developer ID, so a Safari-downloaded
					DMG triggers macOS's <em>"Apple could not verify…"</em> dialog. The
					recommended one-line install below uses
					<code class="font-mono text-xs">curl</code>, which doesn't attach the
					<code class="font-mono text-xs">com.apple.quarantine</code> tag that browsers
					do — so Gatekeeper has nothing to flag and the install runs cleanly.
					Apple Developer signing is on the roadmap.
				</Alert.Description>
			</Alert.Root>

			<Alert.Root class="mt-3 border-amber-500/30 bg-amber-500/5">
				<Alert.Title>Apple Silicon only</Alert.Title>
				<Alert.Description>
					The desktop app ships for Apple Silicon (M1/M2/M3/M4) Macs only as
					of v0.11.x. If you're on an Intel Mac, the CLI agent still works
					— see <a href="/install.sh" class="font-medium text-primary hover:underline">/install.sh</a> for
					the universal CLI installer, and use any browser to access the
					dashboard.
				</Alert.Description>
			</Alert.Root>

			<Tabs.Root value="recommended" class="mt-4 w-full">
				<Tabs.List class="grid w-full grid-cols-2">
					<Tabs.Trigger value="recommended">Recommended (one command)</Tabs.Trigger>
					<Tabs.Trigger value="manual">Manual (download DMG)</Tabs.Trigger>
				</Tabs.List>

				<Tabs.Content value="recommended">
					<Card.Root>
						<Card.Header>
							<Card.Title>One-line install</Card.Title>
							<Card.Description>
								Paste this into Terminal. Installs into <code class="font-mono text-xs">/Applications</code>
								and launches automatically. Requires your admin password.
							</Card.Description>
						</Card.Header>
						<Card.Content class="space-y-3">
							<div class="rounded-md border bg-muted/30 p-3">
								<code class="block break-all font-mono text-[12px]">{macInstallCmd}</code>
							</div>
							<Button variant="outline" size="sm" onclick={() => copy(macInstallCmd, 'mac-install')}>
								{copied === 'mac-install' ? 'Copied!' : 'Copy command'}
							</Button>
						</Card.Content>
					</Card.Root>
				</Tabs.Content>

				<Tabs.Content value="manual">
					<Card.Root>
						<Card.Header>
							<Card.Title>Download the DMG</Card.Title>
							<Card.Description>
								Apple Silicon. You'll need to unblock Gatekeeper once.
							</Card.Description>
						</Card.Header>
						<Card.Content class="space-y-4">
							<Button variant="default" class="w-full" href="/download/desktop/hopssh-macos-aarch64.dmg">
								Download for Apple Silicon
							</Button>

							<div class="space-y-3 rounded-md border bg-muted/20 p-4 text-xs">
								<p class="font-medium text-foreground">After downloading:</p>
								<ol class="list-decimal space-y-2 pl-5 leading-relaxed text-muted-foreground">
									<li>Double-click the DMG to mount it.</li>
									<li>Drag <code class="font-mono">hopssh.app</code> into <code class="font-mono">Applications</code>.</li>
									<li>Eject the DMG.</li>
									<li>
										Open <code class="font-mono">Applications</code> in Finder and double-click
										<code class="font-mono">hopssh</code>. macOS shows
										<em>"Apple could not verify…"</em> and refuses to open it.
										<strong>This is expected.</strong> Use one of the unblock paths below.
									</li>
								</ol>
							</div>

							<div class="space-y-3 rounded-md border border-amber-500/20 bg-amber-500/5 p-4 text-xs">
								<p class="font-medium text-foreground">Unblock Gatekeeper (Terminal, fastest):</p>
								<div class="rounded-md border bg-background p-2">
									<code class="block break-all font-mono text-[11px]">{macXattrCmd}</code>
								</div>
								<Button variant="outline" size="sm" onclick={() => copy(macXattrCmd, 'mac-xattr')}>
									{copied === 'mac-xattr' ? 'Copied!' : 'Copy command'}
								</Button>
							</div>
						</Card.Content>
					</Card.Root>
				</Tabs.Content>
			</Tabs.Root>
		</Tabs.Content>

		<!-- ============= Windows ============= -->
		<Tabs.Content value="windows">
			<Alert.Root class="mt-4">
				<Alert.Title>Foundation build — verify before deploying</Alert.Title>
				<Alert.Description>
					The Windows desktop client is in the <strong>foundation</strong> phase: it builds in CI but
					hasn't been hardware-verified yet. The MSI / NSIS installers are
					expected to install correctly, but UX details (tray rendering,
					autostart on login, system-mode UAC dialog) need a real Windows
					machine to confirm. Use it on a test machine first; please file
					<a href="https://github.com/trustos/hopssh/issues" class="font-medium text-primary hover:underline">issues</a>
					if anything breaks.
				</Alert.Description>
			</Alert.Root>

			<Card.Root class="mt-4">
				<Card.Header>
					<Card.Title>Download for Windows</Card.Title>
					<Card.Description>
						x86_64 (64-bit Intel / AMD). ARM64 builds are deferred.
					</Card.Description>
				</Card.Header>
				<Card.Content class="space-y-4">
					<div class="grid grid-cols-1 gap-2 sm:grid-cols-2">
						<Button variant="default" href="/download/desktop/hopssh-windows-x86_64.msi">
							Download MSI installer
						</Button>
						<Button variant="outline" href="/download/desktop/hopssh-windows-x86_64-setup.exe">
							Download NSIS installer
						</Button>
					</div>

					<div class="space-y-3 rounded-md border bg-muted/20 p-4 text-xs">
						<p class="font-medium text-foreground">Choosing between MSI and NSIS:</p>
						<ul class="list-disc space-y-1 pl-5 leading-relaxed text-muted-foreground">
							<li><strong>MSI</strong> — preferred for IT-managed deployments (Group Policy, Intune).
								Per-machine install by default.</li>
							<li><strong>NSIS (.exe)</strong> — preferred for personal installs. Per-user install,
								smaller, simpler.</li>
						</ul>
					</div>

					<div class="space-y-3 rounded-md border border-amber-500/20 bg-amber-500/5 p-4 text-xs">
						<p class="font-medium text-foreground">SmartScreen warning</p>
						<p class="text-muted-foreground">
							Windows Defender SmartScreen will warn about an unsigned
							publisher. Click <strong>More info</strong> → <strong>Run anyway</strong> to proceed.
							Code-signing with an EV Authenticode certificate is on the
							roadmap.
						</p>
					</div>
				</Card.Content>
			</Card.Root>
		</Tabs.Content>

		<!-- ============= Linux ============= -->
		<Tabs.Content value="linux">
			<Alert.Root class="mt-4">
				<Alert.Title>Foundation build — verify before deploying</Alert.Title>
				<Alert.Description>
					The Linux desktop client is in the <strong>foundation</strong> phase: it builds in CI
					(Ubuntu 22.04 with WebKit2GTK 4.1) but hasn't been hardware-verified
					on every distro. AppImage is the most portable option; .deb / .rpm
					are tested on Ubuntu / Fedora respectively.
				</Alert.Description>
			</Alert.Root>

			<Card.Root class="mt-4">
				<Card.Header>
					<Card.Title>Download for Linux ({linuxArch})</Card.Title>
					<Card.Description>
						{#if linuxArch === 'aarch64'}
							Detected ARM64 (aarch64) browser — the buttons below default
							to the ARM64 build. Switch to x86_64 if you need the Intel/AMD
							desktop build instead.
						{:else}
							Detected x86_64 browser — the buttons below default to the
							64-bit Intel/AMD build. Switch to aarch64 if you're on an
							ARM machine (Raspberry Pi 4/5, AWS Graviton, Apple Silicon
							running Linux in a VM).
						{/if}
					</Card.Description>
				</Card.Header>
				<Card.Content class="space-y-4">
					<div class="flex gap-2 text-xs">
						<span class="font-medium text-muted-foreground">Architecture:</span>
						<button
							class={linuxArch === 'x86_64'
								? 'rounded bg-primary/15 px-2 py-0.5 font-medium text-primary'
								: 'rounded bg-transparent px-2 py-0.5 text-muted-foreground hover:bg-muted'}
							onclick={() => (linuxArch = 'x86_64')}
						>x86_64</button>
						<button
							class={linuxArch === 'aarch64'
								? 'rounded bg-primary/15 px-2 py-0.5 font-medium text-primary'
								: 'rounded bg-transparent px-2 py-0.5 text-muted-foreground hover:bg-muted'}
							onclick={() => (linuxArch = 'aarch64')}
						>aarch64 (ARM64)</button>
					</div>

					<div class="grid grid-cols-1 gap-2 sm:grid-cols-3">
						<Button variant="default" href="/download/desktop/hopssh-linux-{linuxArch}.AppImage">
							AppImage (universal)
						</Button>
						<Button variant="outline" href="/download/desktop/hopssh-linux-{linuxArch}.deb">
							.deb (Debian / Ubuntu)
						</Button>
						<Button variant="outline" href="/download/desktop/hopssh-linux-{linuxArch}.rpm">
							.rpm (Fedora / RHEL)
						</Button>
					</div>

					<div class="space-y-3 rounded-md border bg-muted/20 p-4 text-xs">
						<p class="font-medium text-foreground">After downloading the AppImage:</p>
						<pre class="overflow-x-auto rounded bg-background p-2 font-mono text-[11px]">{`chmod +x hopssh-linux-${linuxArch}.AppImage
./hopssh-linux-${linuxArch}.AppImage`}</pre>
					</div>

					<div class="space-y-3 rounded-md border bg-muted/20 p-4 text-xs">
						<p class="font-medium text-foreground">.deb (Debian / Ubuntu):</p>
						<pre class="overflow-x-auto rounded bg-background p-2 font-mono text-[11px]">{`sudo apt install ./hopssh-linux-${linuxArch}.deb`}</pre>
					</div>

					<div class="space-y-3 rounded-md border bg-muted/20 p-4 text-xs">
						<p class="font-medium text-foreground">.rpm (Fedora / RHEL / openSUSE):</p>
						<pre class="overflow-x-auto rounded bg-background p-2 font-mono text-[11px]">{`sudo dnf install ./hopssh-linux-${linuxArch}.rpm`}</pre>
					</div>

					<div class="space-y-3 rounded-md border border-amber-500/20 bg-amber-500/5 p-4 text-xs">
						<p class="font-medium text-foreground">Runtime dependencies</p>
						<p class="text-muted-foreground">
							The desktop app needs <code class="font-mono">libwebkit2gtk-4.1</code>,
							<code class="font-mono">libgtk-3</code>, and
							<code class="font-mono">libayatana-appindicator3</code> at runtime.
							The .deb / .rpm declare these as dependencies; the AppImage
							assumes they're already installed (default on most modern
							desktops).
						</p>
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
