<script lang="ts">
  import { untrack } from 'svelte';
  import { SvelteSet } from 'svelte/reactivity';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { ApiError, errorMessage, get, post, qs } from '#lib/api.js';
  import { formatDate, formatSize, isViewableImage, kindOf, thumbable } from '#lib/format.js';
  import type { Entry, ShareInfo } from '#lib/types.js';
  import { Uploader, filesFromDrop, filesFromInput } from '#lib/upload.svelte.js';
  import Icon from '#lib/components/Icon.svelte';

  const token = $derived(page.params.token ?? '');
  const base = $derived('/s/' + encodeURIComponent(token));
  const sub = $derived(page.url.searchParams.get('path') ?? '');

  let info = $state<ShareInfo | null>(null);
  let fatal = $state('');
  let password = $state('');
  let pwErr = $state('');
  let busy = $state(false);

  let entries = $state<Entry[]>([]);
  let listErr = $state('');
  let preview = $state<Entry | null>(null);
  const failedThumbs = new SvelteSet<string>();

  // Inline previews count against a download limit, so do not load them unasked.
  let showPreview = $state(false);
  let uploader = $state<Uploader | null>(null);
  let fileInput: HTMLInputElement | undefined = $state();
  let dragging = $state(0);

  async function loadInfo() {
    fatal = '';
    try {
      info = await get<ShareInfo>(base + '/api/info');
      if (info.kind === 'upload' && !uploader) {
        uploader = new Uploader('/_upload/', { 'X-Share-Token': token });
      }
    } catch (e) {
      fatal =
        e instanceof ApiError && e.status === 404
          ? 'This link is invalid or has expired.'
          : errorMessage(e);
    }
  }

  async function loadList(p: string) {
    listErr = '';
    try {
      const r = await get<{ path: string; entries: Entry[] | null }>(
        base + '/api/list' + qs({ path: p })
      );
      entries = r.entries ?? [];
    } catch (e) {
      listErr = errorMessage(e);
      entries = [];
    }
  }

  $effect(() => {
    void token;
    untrack(loadInfo);
  });

  const unlocked = $derived(!!info && (!info.needs_password || info.unlocked));

  $effect(() => {
    const p = sub;
    if (info && unlocked && info.kind === 'download' && info.is_dir) untrack(() => loadList(p));
  });

  async function unlock(e: Event) {
    e.preventDefault();
    busy = true;
    pwErr = '';
    try {
      await post(base + '/api/unlock', { password });
      password = '';
      await loadInfo();
    } catch (x) {
      pwErr = errorMessage(x);
    } finally {
      busy = false;
    }
  }

  function openDir(p: string) {
    goto(base + qs({ path: p || undefined }));
  }

  function fileURL(p: string, inline = false) {
    return base + '/file' + qs({ path: p || undefined, inline });
  }

  const crumbs = $derived(sub ? sub.split('/') : []);

  function pick() {
    if (!fileInput || !uploader) return;
    const files = filesFromInput(fileInput.files);
    if (files.length) uploader.add(files, '');
    fileInput.value = '';
  }
  async function onDrop(e: DragEvent) {
    dragging = 0;
    if (!uploader || !e.dataTransfer) return;
    e.preventDefault();
    const files = await filesFromDrop(e.dataTransfer);
    if (files.length) uploader.add(files, '');
  }

  function iconFor(e: { is_dir: boolean; mime?: string }): string {
    const k = kindOf(e);
    return k === 'dir' ? 'folder' : k === 'file' ? 'file' : k;
  }
</script>

<svelte:head><title>{info?.name || 'Shared files'}</title></svelte:head>

<div class="share">
  {#if fatal}
    <div class="card center">
      <Icon name="lock" size={40} />
      <h1>Link unavailable</h1>
      <p class="muted">{fatal}</p>
    </div>
  {:else if !info}
    <p class="muted center">Loading…</p>
  {:else if !unlocked}
    <div class="card center narrow">
      <Icon name="lock" size={40} />
      <h1>{info.name}</h1>
      <p class="muted">This link is password protected.</p>
      <form onsubmit={unlock}>
        <!-- svelte-ignore a11y_autofocus -->
        <input
          type="password"
          bind:value={password}
          placeholder="Password"
          required
          autofocus
          autocomplete="off"
        />
        <button class="primary" disabled={busy}>Unlock</button>
      </form>
      {#if pwErr}<div class="error">{pwErr}</div>{/if}
    </div>
  {:else if info.kind === 'upload'}
    <div
      class="card drop"
      class:over={dragging > 0}
      role="region"
      aria-label="Upload area"
      ondragenter={(e) => (e.preventDefault(), dragging++)}
      ondragover={(e) => e.preventDefault()}
      ondragleave={() => (dragging = Math.max(0, dragging - 1))}
      ondrop={onDrop}
    >
      <Icon name="upload" size={48} />
      <h1>Upload files to "{info.name}"</h1>
      <p class="muted">
        Drag and drop files here, or choose them. You will not be able to see other files in this
        folder.
      </p>
      <button class="primary" onclick={() => fileInput?.click()}>Choose files</button>
      <input type="file" multiple hidden bind:this={fileInput} onchange={pick} />
      <p class="muted small">Link expires {formatDate(info.expires_at)}</p>
    </div>
    {#if uploader && uploader.items.length}
      <div class="card">
        <ul class="ups">
          {#each uploader.items as it (it.id)}
            <li>
              <div class="row">
                <span class="nm">{it.relativePath}</span>
                <span class="spacer"></span>
                <span class="small muted">
                  {#if it.status === 'done'}✓ uploaded{it.storedName && it.storedName !== it.name
                      ? ` as ${it.storedName}`
                      : ''}
                  {:else if it.status === 'uploading'}{Math.floor(
                      (it.sent / Math.max(it.size, 1)) * 100
                    )}%
                  {:else}{it.status}{/if}
                </span>
                {#if it.status === 'uploading' || it.status === 'queued'}
                  <button class="icon" aria-label="Cancel" onclick={() => uploader?.cancel(it.id)}
                    ><Icon name="close" size={16} /></button
                  >
                {/if}
              </div>
              {#if it.status === 'uploading'}<div class="progress">
                  <div style="width:{(it.sent / Math.max(it.size, 1)) * 100}%"></div>
                </div>{/if}
              {#if it.error}<div class="small err">{it.error}</div>{/if}
            </li>
          {/each}
        </ul>
      </div>
    {/if}
  {:else if !info.is_dir}
    {@const k = kindOf({ is_dir: false, mime: info.mime })}
    <div class="card file">
      <div class="row">
        <Icon name={iconFor({ is_dir: false, mime: info.mime })} size={28} />
        <div>
          <h1 class="fname">{info.name}</h1>
          <span class="muted small"
            >{formatSize(info.size)} · expires {formatDate(info.expires_at)}
            {#if info.downloads_left !== undefined}
              · {info.downloads_left} downloads left{/if}</span
          >
        </div>
        <span class="spacer"></span>
        <a class="btn primary" href={fileURL('')} download
          ><Icon name="download" size={16} />Download</a
        >
      </div>
      <div class="media">
        {#if info.downloads_left !== undefined && !showPreview && (k === 'image' || k === 'video' || k === 'audio' || k === 'pdf')}
          <p class="center"><button onclick={() => (showPreview = true)}>Show preview</button></p>
          <p class="center muted small">Previewing uses one of the remaining downloads.</p>
        {:else if k === 'image' && isViewableImage(info.mime)}
          <img src={fileURL('', true)} alt={info.name} />
        {:else if k === 'video'}
          <!-- svelte-ignore a11y_media_has_caption -->
          <video controls playsinline preload="metadata" src={fileURL('', true)}></video>
        {:else if k === 'audio'}
          <audio controls preload="metadata" src={fileURL('', true)}></audio>
        {:else if k === 'pdf'}
          <iframe src={fileURL('', true)} title={info.name}></iframe>
        {/if}
      </div>
    </div>
  {:else}
    <div class="head row">
      <nav class="crumbs">
        <button class="ghost small" onclick={() => openDir('')}
          ><Icon name="folder" size={18} />{info.name}</button
        >
        {#each crumbs as c, i (i)}
          <span class="muted">/</span>
          <button class="ghost small" onclick={() => openDir(crumbs.slice(0, i + 1).join('/'))}
            >{c}</button
          >
        {/each}
      </nav>
      <span class="spacer"></span>
      <a
        class="btn primary small"
        href={base + '/zip' + qs({ paths: sub ? [sub] : undefined })}
        download
      >
        <Icon name="download" size={16} />Download {sub ? 'folder' : 'all'}
      </a>
    </div>
    <p class="muted small">
      Shared folder · expires {formatDate(info.expires_at)}{info.downloads_left !== undefined
        ? ` · ${info.downloads_left} downloads left`
        : ''}
    </p>
    {#if listErr}<div class="error">{listErr}</div>{/if}
    <div class="grid">
      {#each entries as e (e.path)}
        <div class="tile">
          <button
            class="ghost tbtn"
            onclick={() => (e.is_dir ? openDir(e.path) : (preview = e))}
            title={e.name}
          >
            <div class="thumb">
              {#if thumbable(e) && !failedThumbs.has(e.path)}
                <img
                  loading="lazy"
                  alt=""
                  src={base + '/thumb' + qs({ path: e.path })}
                  onerror={() => failedThumbs.add(e.path)}
                />
              {:else}
                <span class="ic" class:dir={e.is_dir}><Icon name={iconFor(e)} size={44} /></span>
              {/if}
            </div>
            <div class="tname">{e.name}</div>
            <div class="tsize muted small">{e.is_dir ? '' : formatSize(e.size)}</div>
          </button>
        </div>
      {:else}
        {#if !listErr}<p class="muted">This folder is empty.</p>{/if}
      {/each}
    </div>
  {/if}
</div>

{#if preview}
  {@const k = kindOf(preview)}
  <div class="overlay" role="dialog" aria-modal="true" aria-label={preview.name}>
    <header class="row">
      <strong class="oname">{preview.name}</strong>
      <span class="spacer"></span>
      <a class="btn small" href={fileURL(preview.path)} download
        ><Icon name="download" size={16} />Download</a
      >
      <button class="icon" aria-label="Close" onclick={() => (preview = null)}
        ><Icon name="close" /></button
      >
    </header>
    <div class="ostage">
      {#if k === 'image' && isViewableImage(preview.mime)}
        <img src={fileURL(preview.path, true)} alt={preview.name} />
      {:else if k === 'video'}
        <!-- svelte-ignore a11y_media_has_caption -->
        <video controls autoplay playsinline src={fileURL(preview.path, true)}></video>
      {:else if k === 'audio'}
        <audio controls autoplay src={fileURL(preview.path, true)}></audio>
      {:else if k === 'pdf'}
        <iframe src={fileURL(preview.path, true)} title={preview.name}></iframe>
      {:else}
        <div class="center">
          <p>No preview available.</p>
          <a class="btn primary" href={fileURL(preview.path)} download
            >Download ({formatSize(preview.size)})</a
          >
        </div>
      {/if}
    </div>
  </div>
{/if}

<svelte:window onkeydown={(e) => e.key === 'Escape' && (preview = null)} />

<style>
  .share {
    max-width: 1100px;
    margin: 0 auto;
    padding: 1.5rem 1rem 4rem;
  }
  .center {
    text-align: center;
  }
  .narrow {
    max-width: 420px;
    margin: 3rem auto;
  }
  .narrow form {
    display: flex;
    gap: 0.5rem;
    justify-content: center;
    margin-top: 1rem;
  }
  h1 {
    margin: 0.5rem 0;
  }
  .drop {
    text-align: center;
    padding: 3rem 1rem;
    border: 2px dashed var(--border);
  }
  .drop.over {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .ups {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .ups li {
    padding: 0.4rem 0;
    border-bottom: 1px solid var(--border);
  }
  .ups li:last-child {
    border-bottom: none;
  }
  .nm {
    word-break: break-all;
  }
  .err {
    color: var(--danger);
  }
  .fname {
    font-size: 1.15rem;
    margin: 0;
    word-break: break-word;
  }
  .media {
    margin-top: 1rem;
  }
  .media img,
  .media video {
    width: 100%;
    aspect-ratio: 16 / 9;
    background: #000;
    max-width: 100%;
    max-height: 75vh;
    display: block;
    margin: 0 auto;
    border-radius: var(--radius);
  }
  .media audio {
    width: 100%;
  }
  .media iframe {
    width: 100%;
    height: 80vh;
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }
  .head {
    margin-bottom: 0.25rem;
  }
  .crumbs {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.1rem;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
    gap: 0.75rem;
    margin-top: 0.75rem;
  }
  .tile {
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .tbtn {
    display: block;
    width: 100%;
    padding: 0;
    border: none;
    border-radius: 0;
    white-space: normal;
    text-align: left;
  }
  .thumb {
    aspect-ratio: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    background: var(--surface-2);
    overflow: hidden;
  }
  .thumb img {
    width: 100%;
    height: 100%;
    object-fit: cover;
  }
  .ic {
    color: var(--muted);
    display: inline-flex;
  }
  .ic.dir {
    color: #e0a526;
  }
  .tname {
    padding: 0.4rem 0.5rem 0;
    font-size: 0.85rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .tsize {
    padding: 0 0.5rem 0.4rem;
  }
  .overlay {
    position: fixed;
    inset: 0;
    z-index: 60;
    background: rgba(8, 10, 14, 0.94);
    color: #eee;
    display: flex;
    flex-direction: column;
  }
  .overlay header {
    padding: 0.5rem 0.75rem;
    flex-wrap: nowrap;
  }
  .overlay header .btn,
  .overlay header .icon {
    color: #eee;
    background: transparent;
    border-color: rgba(255, 255, 255, 0.25);
  }
  .oname {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .ostage {
    flex: 1;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 0.75rem;
    min-height: 0;
  }
  .ostage img,
  .ostage video {
    max-width: 100%;
    max-height: 100%;
    object-fit: contain;
  }
  .ostage audio {
    width: min(600px, 100%);
  }
  .ostage iframe {
    width: 100%;
    height: 100%;
    border: none;
    background: #fff;
  }
  @media (max-width: 640px) {
    .grid {
      grid-template-columns: repeat(auto-fill, minmax(110px, 1fr));
    }
  }
</style>
