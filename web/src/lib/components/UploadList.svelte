<script lang="ts">
  import { formatSize } from '#lib/format.js';
  import { type UploadItem, type Uploader, isActive } from '#lib/upload.svelte.js';
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
          {:else if it.status === 'finishing'}finishing…
          {:else if it.status === 'queued'}queued
          {:else if it.status === 'cancelled'}cancelled
          {/if}
        </span>
        {#if isActive(it)}
          <button
            class="icon"
            onclick={() => uploader.cancel(it.id)}
            aria-label={'Cancel ' + it.name}
          >
            <Icon name="close" size={16} />
          </button>
        {:else if uploader.canRetry(it.id)}
          <button class="small" onclick={() => uploader.retry(it.id)}>Retry</button>
        {/if}
      </div>
      {#if it.status === 'uploading'}
        <div
          class="progress"
          role="progressbar"
          aria-label={'Uploading ' + it.name}
          aria-valuemin="0"
          aria-valuemax="100"
          aria-valuenow={Math.floor(pct(it))}
        >
          <div style="width:{pct(it)}%"></div>
        </div>
      {/if}
      {#if it.status === 'error'}<div class="err small" role="alert">{it.error}</div>{/if}
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
