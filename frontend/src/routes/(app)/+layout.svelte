<script lang="ts">
	import AppSidebar from '$lib/components/app-sidebar.svelte';

	let { children } = $props();
</script>

<AppSidebar>
	<!-- min-w-0 + overflow-x-hidden are the canonical shadcn-sidebar
	     containment pattern: SidebarInset is a flex child whose default
	     `min-width: auto` lets descendant content (a wide table) push
	     the layout wider than the viewport, breaking any nested
	     overflow-x-auto containment. Both lines together pin the page
	     to the viewport so horizontal scroll is scoped to the table
	     wrapper instead of leaking up to window-level scroll that
	     would also drag the fixed sidebar. -->
	<div class="min-w-0 w-full overflow-x-hidden">
		{@render children()}
	</div>
</AppSidebar>
