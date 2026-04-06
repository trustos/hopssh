<script lang="ts">
	import AppSidebar from '$lib/components/app-sidebar.svelte';

	let { children } = $props();
</script>

<AppSidebar>
	{@render children()}
</AppSidebar>
