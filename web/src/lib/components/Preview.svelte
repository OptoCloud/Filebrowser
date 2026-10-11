<script lang="ts">
  import { errorMessage, post, qs } from '#lib/api.js';
  import { TEXT_PREVIEW_MAX, formatSize, isViewableImage, kindOf } from '#lib/format.js';
  import type { Entry } from '#lib/types.js';
  import Icon from './Icon.svelte';
  import MediaPlayer from './MediaPlayer.svelte';

  let {
    entries,
    index = $bindable(),
    onclose,
  }: { entries: Entry[]; index: number; onclose: () => void } = $props();

  const entry = $derived(entries[index]);
  const kind = $derived(entry ? kindOf(entry) : 'file');
  const downloadURL = $derived(entry ? '/api/files/download' + qs({ path: entry.path }) : '');
  const inlineURL = $derived(
    entry ? '/api/files/download' + qs({ path: entry.path, inline: 1 }) : ''
  );

  let text = $state<string | null>(null);
  let pdfURL = $state('');
  let err = $state('');

  $effect(() => {
    const e = entry;
    text = null;
    pdfURL = '';
    err = '';
    if (!e) return;
    const k = kindOf(e);
    if (k === 'text' && e.size <= TEXT_PREVIEW_MAX) {
      fetch('/api/files/download' + qs({ path: e.path }), { credentials: 'same-origin' })
        .then((r) => (r.ok ? r.text() : Promise.reject(new Error(`HTTP ${r.status}`))))
        .then((t) => {
          if (entry === e) text = t;
        })
        .catch((x) => (err = errorMessage(x)));
    } else if (k === 'pdf') {
      post<{ url: string }>('/api/files/preview-url', { path: e.path })
        .then((r) => {
          if (entry === e) pdfURL = r.url;
        })
        .catch((x) => (err = errorMessage(x)));
    }
  });

  function step(d: number) {
    const n = entries.length;
    for (let i = 1; i < n; i++) {
      const j = (index + d * i + n) % n;
      if (!entries[j].is_dir) {
        index = j;
        return;
      }
    }
  }

  function onkeydown(e: KeyboardEvent) {
    const t = e.target as HTMLElement;
    if (t && (t.tagName === 'INPUT' || t.tagName === 'SELECT' || t.tagName === 'VIDEO')) return;
    if (e.key === 'Escape') onclose();
    else if (e.key === 'ArrowLeft') step(-1);
    else if (e.key === 'ArrowRight') step(1);
  }
</script>

<svelte:window {onkeydown} />

{#if entry}
  <div class="overlay" role="dialog" aria-modal="true" aria-label={entry.name}>
    <header>
      <span class="title" title={entry.name}>{entry.name}</span>
      <span class="muted small hide-mobile">{formatSize(entry.size)}</span>
      <span class="spacer"></span>
      <a class="btn small" href={downloadURL} download
        ><Icon name="download" size={16} /><span class="hide-mobile">Download</span></a
      >
      <button class="icon" onclick={onclose} aria-label="Close"><Icon name="close" /></button>
    </header>
    <div class="stage">
      {#if entries.filter((e) => !e.is_dir).length > 1}
        <button class="nav left" onclick={() => step(-1)} aria-label="Previous"
          ><Icon name="prev" size={32} /></button
        >
        <button class="nav right" onclick={() => step(1)} aria-label="Next"
          ><Icon name="next" size={32} /></button
        >
      {/if}
      <div class="content">
        {#if err}
          <div class="error">{err}</div>
        {:else if kind === 'image' && isViewableImage(entry.mime)}
          <img src={inlineURL} alt={entry.name} />
        {:else if kind === 'video' || kind === 'audio'}
          {#key entry.path}
            <MediaPlayer path={entry.path} mime={entry.mime || ''} />
          {/key}
        {:else if kind === 'pdf'}
          {#if pdfURL}
            <iframe src={pdfURL} title={entry.name}></iframe>
          {:else}
            <p class="muted">Loading…</p>
          {/if}
        {:else if kind === 'text' && entry.size <= TEXT_PREVIEW_MAX}
          {#if text === null}<p class="muted">Loading…</p>{:else}<pre>{text}</pre>{/if}
        {:else}
          <div class="none">
            <Icon name="file" size={64} />
            <p>No preview available for this file.</p>
            <a class="btn primary" href={downloadURL} download
              ><Icon name="download" size={16} />Download</a
            >
          </div>
        {/if}
      </div>
    </div>
  </div>
{/if}

<style>
  .overlay {
    position: fixed;
    inset: 0;
    z-index: 60;
    background: rgba(8, 10, 14, 0.94);
    color: #eee;
    display: flex;
    flex-direction: column;
  }
  header {
    display: flex;
    align-items: center;
    gap: 0.6rem;
    padding: 0.5rem 0.75rem;
    background: rgba(0, 0, 0, 0.4);
  }
  header .icon,
  header .btn {
    color: #eee;
    background: transparent;
    border-color: rgba(255, 255, 255, 0.25);
  }
  .title {
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .stage {
    flex: 1;
    position: relative;
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 0;
    padding: 0.75rem 3.5rem;
  }
  .content {
    width: 100%;
    height: 100%;
    display: flex;
    align-items: center;
    justify-content: center;
    min-height: 0;
  }
  img {
    max-width: 100%;
    max-height: 100%;
    object-fit: contain;
  }
  iframe {
    width: 100%;
    height: 100%;
    border: none;
    background: #fff;
    border-radius: 4px;
  }
  pre {
    width: 100%;
    height: 100%;
    overflow: auto;
    margin: 0;
    padding: 1rem;
    background: var(--surface);
    color: var(--text);
    border-radius: var(--radius);
    font-size: 0.85rem;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .content :global(.player) {
    max-width: 1200px;
    color: #eee;
  }
  .content :global(.player .muted) {
    color: #bbb;
  }
  .nav {
    position: absolute;
    top: 50%;
    transform: translateY(-50%);
    background: rgba(255, 255, 255, 0.08);
    border: none;
    color: #fff;
    border-radius: 50%;
    padding: 0.4rem;
    z-index: 2;
  }
  .nav:hover {
    background: rgba(255, 255, 255, 0.2) !important;
  }
  .nav.left {
    left: 0.5rem;
  }
  .nav.right {
    right: 0.5rem;
  }
  .none {
    text-align: center;
  }
  @media (max-width: 640px) {
    .stage {
      padding: 0.5rem;
    }
    .nav {
      top: auto;
      bottom: 0.5rem;
      transform: none;
    }
  }
</style>
