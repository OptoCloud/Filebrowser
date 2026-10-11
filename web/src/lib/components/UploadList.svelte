<script lang="ts">
  import { formatSize } from '#lib/format.js';
  import type { UploadItem, Uploader } from '#lib/upload.svelte.js';
  import Icon from './Icon.svelte';

  let { uploader }: { uploader: Uploader } = $props();

  function pct(it: UploadItem): number {
    return (it.sent / Math.max(it.size, 1)) * 100;
  }
</script>

<ul>
  {#each uploader.items as it (it.id)}
    <li>
      <div class="line">
        <span class="name" title={it.relativePath}>{it.relativePath}</span>
        <span class="muted small">
          {#if it.status === 'uploading'}{Math.floor(pct(it))}%
          {:else if it.status === 'done'}✓ {it.storedName && it.storedName !== it.name
              ? `as ${it.storedName}`
              : formatSize(it.size)}
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
        <div class="progress"><div style="width:{pct(it)}%"></div></div>
      {/if}
      {#if it.status === 'error'}<div class="err small">{it.error}</div>{/if}
    </li>
  {/each}
</ul>

<style>
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
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
