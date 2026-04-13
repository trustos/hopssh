<script lang="ts">
	import { page } from '$app/state';

	const proxyUrl = $derived(
		`/api/networks/${page.params.networkID}/nodes/${page.params.nodeID}/proxy/${page.params.port}/`
	);

	let status = $state<'checking' | 'ok' | 'error'>('checking');
	let errorMessage = $state('');

	async function checkProxy(url: string) {
		status = 'checking';
		errorMessage = '';
		try {
			const resp = await fetch(url, { method: 'HEAD', credentials: 'include' });
			if (resp.ok) {
				status = 'ok';
			} else if (resp.status === 502) {
				status = 'error';
				errorMessage = `Nothing is listening on port ${page.params.port}. Make sure the service is running on the node.`;
			} else if (resp.status === 401) {
				status = 'error';
				errorMessage = 'Session expired. Please log in again.';
			} else if (resp.status === 403) {
				status = 'error';
				errorMessage = 'Port forwarding is not enabled for this node. Enable it in the dashboard.';
			} else if (resp.status === 404) {
				status = 'error';
				errorMessage = 'Node not found. It may have been removed from the network.';
			} else {
				status = 'error';
				errorMessage = `Proxy returned ${resp.status}: ${resp.statusText}`;
			}
		} catch {
			status = 'error';
			errorMessage = 'Could not reach the control plane. Check your connection.';
		}
	}

	$effect(() => {
		checkProxy(proxyUrl);
	});
</script>

<svelte:head>
	<title>Proxy — port {page.params.port}</title>
</svelte:head>

{#if status === 'checking'}
	<div class="overlay">
		<p>Connecting to port {page.params.port}...</p>
	</div>
{:else if status === 'error'}
	<div class="overlay">
		<div class="error-card">
			<h2>Connection Failed</h2>
			<p>{errorMessage}</p>
			<div class="actions">
				<button onclick={() => checkProxy(proxyUrl)}>Retry</button>
				<button class="secondary" onclick={() => history.back()}>Go Back</button>
			</div>
		</div>
	</div>
{:else}
	<iframe src={proxyUrl} title="Proxied service on port {page.params.port}"></iframe>
{/if}

<style>
	iframe {
		position: fixed;
		inset: 0;
		width: 100%;
		height: 100%;
		border: none;
	}

	.overlay {
		position: fixed;
		inset: 0;
		display: flex;
		align-items: center;
		justify-content: center;
		background: hsl(var(--background));
		color: hsl(var(--foreground));
		font-family: system-ui, sans-serif;
	}

	.error-card {
		max-width: 28rem;
		text-align: center;
		padding: 2rem;
	}

	.error-card h2 {
		font-size: 1.25rem;
		font-weight: 600;
		margin-bottom: 0.75rem;
	}

	.error-card p {
		color: hsl(var(--muted-foreground));
		margin-bottom: 1.5rem;
		line-height: 1.5;
	}

	.actions {
		display: flex;
		gap: 0.75rem;
		justify-content: center;
	}

	button {
		padding: 0.5rem 1.25rem;
		border-radius: 0.375rem;
		font-size: 0.875rem;
		font-weight: 500;
		cursor: pointer;
		border: none;
		background: hsl(var(--primary));
		color: hsl(var(--primary-foreground));
	}

	button:hover {
		opacity: 0.9;
	}

	button.secondary {
		background: transparent;
		border: 1px solid hsl(var(--border));
		color: hsl(var(--foreground));
	}

	button.secondary:hover {
		background: hsl(var(--accent));
	}
</style>
