<script lang="ts">
  import { untrack } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { ApiError, errorMessage, get, post, qs } from '#lib/api.js';
  import { modal } from '#lib/dialog.js';
  import { formatDate, formatSize, isViewableImage, kindOf, pathCrumbs } from '#lib/format.js';
  import { LatestRequest } from '#lib/latest.js';
  import type { Entry, ShareInfo } from '#lib/types.js';
  import { Uploader, filesFromDrop, filesFromInput } from '#lib/upload.svelte.js';
  import EntryIcon from '#lib/components/EntryIcon.svelte';
  import EntryThumb from '#lib/components/EntryThumb.svelte';
  import Icon from '#lib/components/Icon.svelte';
  import UploadList from '#lib/components/UploadList.svelte';

  // The layout re-creates this page when the token changes, so it is fixed per instance.
  const token = page.params.token ?? '';
  const base = '/s/' + encodeURIComponent(token);
  const sub = $derived(page.url.searchParams.get('path') ?? '');

  let info = $state<ShareInfo | null>(null);
  let fatal = $state('');
  let password = $state('');
  let pwErr = $state('');
  let busy = $state(false);

  let entries = $state<Entry[]>([]);
  let listErr = $state('');
  let listLoading = $state(false);
  const listReq = new LatestRequest();
  let preview = $state<Entry | null>(null);

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
    const signal = listReq.next();
    listErr = '';
    listLoading = true;
    try {
      const r = await get<{ path: string; entries: Entry[] | null }>(
        base + '/api/list' + qs({ path: p }),
        { signal }
      );
      if (signal.aborted) return;
      entries = r.entries ?? [];
    } catch (e) {
      if (signal.aborted) return;
      listErr = errorMessage(e);
      entries = [];
    }
    listLoading = false;
  }

  $effect(() => {
    untrack(loadInfo);
    // Leaving the share stops its uploads; they could not be shown anywhere else.
    return () => {
      listReq.abort();
      uploader?.cancelAll();
    };
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

  const crumbs = $derived(pathCrumbs(sub));

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

  // Closing the tab would abort running uploads.
  function onbeforeunload(e: BeforeUnloadEvent) {
    if (uploader?.busy) e.preventDefault();
  }
</script>

<svelte:head><title>{info?.name || 'Shared files'}</title></svelte:head>

<svelte:window {onbeforeunload} />

{#snippet media(p: string, name: string, mime: string | undefined, autoplay: boolean)}
  {@const k = kindOf({ is_dir: false, mime })}
  {#if k === 'image' && isViewableImage(mime)}
    <img src={fileURL(p, true)} alt={name} />
  {:else if k === 'video'}
    <!-- svelte-ignore a11y_media_has_caption -->
    <video controls {autoplay} playsinline preload="metadata" src={fileURL(p, true)}></video>
  {:else if k === 'audio'}
    <audio controls {autoplay} preload="metadata" src={fileURL(p, true)}></audio>
  {:else if k === 'pdf'}
    <iframe src={fileURL(p, true)} title={name}></iframe>
  {/if}
{/snippet}

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
      <div class="card"><UploadList {uploader} /></div>
    {/if}
  {:else if !info.is_dir}
    {@const k = kindOf({ is_dir: false, mime: info.mime })}
    <div class="card file">
      <div class="row">
        <EntryIcon entry={{ is_dir: false, mime: info.mime }} size={28} />
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
        {:else}
          {@render media('', info.name, info.mime, false)}
        {/if}
      </div>
    </div>
  {:else}
    <div class="head row">
      <nav class="crumbs">
        <button class="ghost small" onclick={() => openDir('')}
          ><Icon name="folder" size={18} />{info.name}</button
        >
        {#each crumbs as c (c.path)}
          <span class="muted">/</span>
          <button class="ghost small" onclick={() => openDir(c.path)}>{c.name}</button>
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
            <EntryThumb entry={e} src={base + '/thumb' + qs({ path: e.path })} size={44} />
            <div class="tname">{e.name}</div>
            <div class="tsize muted small">{e.is_dir ? '' : formatSize(e.size)}</div>
          </button>
        </div>
      {:else}
        {#if listLoading}<p class="muted">Loading…</p>
        {:else if !listErr}<p class="muted">This folder is empty.</p>{/if}
      {/each}
    </div>
  {/if}
</div>

{#if preview}
  {@const k = kindOf(preview)}
  <dialog class="viewer" aria-label={preview.name} {@attach modal(() => (preview = null))}>
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
      {#if (k === 'image' && isViewableImage(preview.mime)) || k === 'video' || k === 'audio' || k === 'pdf'}
        {@render media(preview.path, preview.name, preview.mime, true)}
      {:else}
        <div class="center">
          <p>No preview available.</p>
          <a class="btn primary" href={fileURL(preview.path)} download
            >Download ({formatSize(preview.size)})</a
          >
        </div>
      {/if}
    </div>
  </dialog>
{/if}

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
  .viewer header {
    padding: 0.5rem 0.75rem;
    flex-wrap: nowrap;
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
