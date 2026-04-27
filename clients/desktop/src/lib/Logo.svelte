<script lang="ts">
  let { class: cls = '' }: { class?: string } = $props();
</script>

<svg
  class={cls}
  viewBox="0 0 24 24"
  fill="none"
  xmlns="http://www.w3.org/2000/svg"
  aria-hidden="true"
>
  <!-- Stylized hop / mesh node motif: three hops connecting -->
  <circle cx="6" cy="12" r="2.5" stroke="currentColor" stroke-width="1.6" />
  <circle cx="12" cy="6" r="2.5" stroke="currentColor" stroke-width="1.6" />
  <circle cx="18" cy="12" r="2.5" stroke="currentColor" stroke-width="1.6" />
  <circle cx="12" cy="18" r="2.5" stroke="currentColor" stroke-width="1.6" />
  <line x1="8" y1="11" x2="11" y2="8" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
  <line x1="13" y1="8" x2="16" y2="11" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
  <line x1="16" y1="13" x2="13" y2="16" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
  <line x1="11" y1="16" x2="8" y2="13" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
</svg>
