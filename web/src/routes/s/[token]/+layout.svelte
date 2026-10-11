<script lang="ts">
  import { page } from '$app/state';

  let { children } = $props();
</script>

<!-- A different link is a different share: rebuild the page (uploader, listing, preview). -->
{#key page.params.token}
  {@render children()}
{/key}
