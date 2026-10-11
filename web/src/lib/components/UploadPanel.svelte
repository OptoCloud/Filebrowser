<script lang="ts">
  import type { Uploader } from '#lib/upload.svelte.js';
  import { formatSize } from '#lib/format.js';
  import Icon from './Icon.svelte';

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
      <ul>
        {#each uploader.items as it (it.id)}
          <li>
            <div class="line">
              <span class="name" title={it.relativePath}>{it.relativePath}</span>
              <span class="muted small">
                {#if it.status === 'uploading'}{Math.floor((it.sent / Math.max(it.size, 1)) * 100)}%
                {:else if it.status === 'done'}✓ {formatSize(it.size)}
                {:else if it.status === 'queued'}queued
                {:else if it.status === 'cancelled'}cancelled
                {/if}
              </span>
              {#if it.status === 'uploading' || it.status === 'queued'}
                <button class="icon" onclick={() => uploader.cancel(it.id)} aria-label="Cancel">
                  <Icon name="close" size={16} />
                </button>
              {/if}
            </div>
            {#if it.status === 'uploading'}
              <div class="progress">
                <div style="width:{(it.sent / Math.max(it.size, 1)) * 100}%"></div>
              </div>
            {/if}
            {#if it.status === 'error'}<div class="err small">{it.error}</div>{/if}
          </li>
        {/each}
      </ul>
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
  ul {
    list-style: none;
    margin: 0;
    padding: 0.25rem 0.75rem;
    max-height: 40vh;
    overflow-y: auto;
  }
  li {
    padding: 0.35rem 0;
    border-bottom: 1px solid var(--border);
  }
  li:last-child {
    border-bottom: none;
  }
  .line {
    display: flex;
    align-items: center;
    gap: 0.5rem;
  }
  .name {
    flex: 1;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 0.9rem;
  }
  .err {
    color: var(--danger);
  }
</style>
