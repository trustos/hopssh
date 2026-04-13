<script>
	import { page } from '$app/stores';

	$: proxyUrl = `/api/networks/${$page.params.networkID}/nodes/${$page.params.nodeID}/proxy/${$page.params.port}/`;
</script>

<svelte:head>
	<title>Proxy — port {$page.params.port}</title>
</svelte:head>

<iframe src={proxyUrl} title="Proxied service on port {$page.params.port}"></iframe>

<style>
	iframe {
		position: fixed;
		inset: 0;
		width: 100%;
		height: 100%;
		border: none;
	}
</style>
