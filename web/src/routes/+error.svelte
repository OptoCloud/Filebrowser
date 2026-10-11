<script lang="ts">
  import { page } from '$app/state';
</script>

<div class="page">
  <h1>{page.status === 404 ? 'Not found' : 'Something went wrong'}</h1>
  <p class="muted">
    {page.status === 404
      ? "This page doesn't exist, or you don't have access to it."
      : page.error?.message}
  </p>
  {#if !page.url.pathname.startsWith('/s/')}<p><a href="/files">Back to files</a></p>{/if}
</div>
