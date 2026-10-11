<script lang="ts">
  import type { Uploader } from '#lib/upload.svelte.js';
  import Icon from './Icon.svelte';
  import UploadList from './UploadList.svelte';

  let { uploader }: { uploader: Uploader } = $props();
  let collapsed = $state(false);

  const total = $derived(uploader.items.length);
  const done = $derived(uploader.items.filter((i) => i.status === 'done').length);
</script>

{#if total > 0}
  <div class="panel">
    <header>
      <strong>Uploads {done}/{total}</strong>
      <span class="spacer"></span>
      {#if uploader.busy}
        <button class="small" onclick={() => uploader.cancelAll()}>Cancel all</button>
      {:else}
        <button class="small" onclick={() => uploader.clearFinished()}>Clear</button>
      {/if}
      <button class="icon" onclick={() => (collapsed = !collapsed)} aria-label="Toggle">
        <Icon name={collapsed ? 'up' : 'down'} size={18} />
      </button>
    </header>
    {#if !collapsed}
      <div class="list"><UploadList {uploader} /></div>
    {/if}
  </div>
{/if}

<style>
  .panel {
    position: fixed;
    right: 1rem;
    bottom: 1rem;
    width: min(380px, calc(100vw - 2rem));
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    box-shadow: var(--shadow);
    z-index: 40;
  }
  header {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    padding: 0.5rem 0.75rem;
    border-bottom: 1px solid var(--border);
  }
  .list {
    padding: 0.25rem 0.75rem;
    max-height: 40vh;
    overflow-y: auto;
  }
</style>
