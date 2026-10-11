<script lang="ts">
  import { untrack } from 'svelte';
  import { SvelteSet } from 'svelte/reactivity';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { download, errorMessage, get, post, postForm, qs } from '#lib/api.js';
  import { filesHref, formatDate, formatSize, kindOf, parentPath, thumbable } from '#lib/format.js';
  import type { Entry, Listing } from '#lib/types.js';
  import { Uploader, filesFromDrop, filesFromInput } from '#lib/upload.svelte.js';
  import FolderPicker from '#lib/components/FolderPicker.svelte';
  import Icon from '#lib/components/Icon.svelte';
  import Modal from '#lib/components/Modal.svelte';
  import Preview from '#lib/components/Preview.svelte';
  import ShareDialog from '#lib/components/ShareDialog.svelte';
  import UploadPanel from '#lib/components/UploadPanel.svelte';

  type SortKey = 'name' | 'size' | 'mtime';

  const path = $derived(page.params.path ?? '');
  let listing = $state<Listing | null>(null);
  let loading = $state(true);
  let err = $state('');
  let actionErr = $state('');

  let view = $state<'list' | 'grid'>(loadPref('fb.view', 'list') as 'list' | 'grid');
  let sortKey = $state<SortKey>(loadPref('fb.sort', 'name') as SortKey);
  let sortAsc = $state(loadPref('fb.asc', '1') === '1');
  const selected = new SvelteSet<string>();
  let anchor = -1;

  let query = $state('');
  let results = $state<Entry[] | null>(null);
  let truncated = $state(false);
  let searching = $state(false);

  let previewIndex = $state<number | null>(null);
  let mkdirOpen = $state(false);
  let newName = $state('');
  let renameEntry = $state<Entry | null>(null);
  let moveOpen = $state(false);
  let shareEntry = $state<Entry | null>(null);
  let confirmDelete = $state(false);
  let dragging = $state(0);
  const failedThumbs = new SvelteSet<string>();

  let fileInput: HTMLInputElement | undefined = $state();
  let dirInput: HTMLInputElement | undefined = $state();

  let reloadTimer: ReturnType<typeof setTimeout> | undefined;
  const uploader = new Uploader('/api/uploads/', {}, () => {
    clearTimeout(reloadTimer);
    reloadTimer = setTimeout(() => load(path, true), 400);
  });

  function loadPref(k: string, d: string): string {
    try {
      return localStorage.getItem(k) ?? d;
    } catch {
      return d;
    }
  }
  function savePref(k: string, v: string) {
    try {
      localStorage.setItem(k, v);
    } catch {
      /* storage unavailable */
    }
  }

  async function load(p: string, quiet = false) {
    if (!quiet) loading = true;
    err = '';
    try {
      const l = await get<Listing>('/api/files' + qs({ path: p }));
      if (p === path) listing = l;
    } catch (e) {
      if (p === path) {
        listing = null;
        err = errorMessage(e);
      }
    } finally {
      if (p === path) loading = false;
    }
  }

  $effect(() => {
    const p = path;
    untrack(() => {
      selected.clear();
      anchor = -1;
      results = null;
      query = '';
      previewIndex = null;
      load(p);
    });
  });

  $effect(() => {
    if (dirInput) dirInput.webkitdirectory = true;
  });

  const writable = $derived(listing?.permission === 'write');
  const entries = $derived.by(() => {
    const list = results ?? listing?.entries ?? [];
    const dir = sortAsc ? 1 : -1;
    return [...list].sort((a, b) => {
      if (a.is_dir !== b.is_dir) return a.is_dir ? -1 : 1;
      let c = 0;
      if (sortKey === 'size') c = a.size - b.size;
      else if (sortKey === 'mtime') c = new Date(a.mtime).getTime() - new Date(b.mtime).getTime();
      if (c === 0)
        c = a.name.localeCompare(b.name, undefined, { numeric: true, sensitivity: 'base' });
      return c * dir;
    });
  });
  const crumbs = $derived(path ? path.split('/') : []);
  const selection = $derived(entries.filter((e) => selected.has(e.path)));
  // In a traverse-only directory every child is a grant root (or leads to one).
  const canModifySelection = $derived(
    selection.length > 0 && (results !== null || listing?.permission === 'write')
  );

  function setSort(k: SortKey) {
    if (sortKey === k) sortAsc = !sortAsc;
    else {
      sortKey = k;
      sortAsc = k === 'name';
    }
    savePref('fb.sort', sortKey);
    savePref('fb.asc', sortAsc ? '1' : '0');
  }

  function setView(v: 'list' | 'grid') {
    view = v;
    savePref('fb.view', v);
  }

  function toggle(e: Entry, i: number, ev: MouseEvent | KeyboardEvent) {
    if (ev.shiftKey && anchor >= 0) {
      const [a, b] = anchor < i ? [anchor, i] : [i, anchor];
      for (let j = a; j <= b; j++) selected.add(entries[j].path);
    } else if (selected.has(e.path)) selected.delete(e.path);
    else selected.add(e.path);
    anchor = i;
  }

  function toggleAll() {
    if (selected.size === entries.length) selected.clear();
    else entries.forEach((e) => selected.add(e.path));
  }

  function openEntry(e: Entry, i: number) {
    if (e.is_dir) goto(filesHref(e.path));
    else previewIndex = i;
  }

  async function act(fn: () => Promise<unknown>) {
    actionErr = '';
    try {
      await fn();
    } catch (e) {
      actionErr = errorMessage(e);
    }
    await load(path, true);
    if (results !== null) await runSearch();
  }

  async function mkdir(e: Event) {
    e.preventDefault();
    const name = newName.trim();
    mkdirOpen = false;
    newName = '';
    if (name) await act(() => post('/api/files/mkdir', { path, name }));
  }

  async function rename(e: Event) {
    e.preventDefault();
    const ent = renameEntry;
    const name = newName.trim();
    renameEntry = null;
    newName = '';
    if (ent && name && name !== ent.name)
      await act(() => post('/api/files/rename', { path: ent.path, name }));
  }

  async function moveTo(dest: string) {
    const paths = selection.map((e) => e.path);
    moveOpen = false;
    selected.clear();
    await act(() => post('/api/files/move', { paths, dest }));
  }

  async function remove() {
    const paths = selection.map((e) => e.path);
    confirmDelete = false;
    selected.clear();
    await act(() => post('/api/files/delete', { paths }));
  }

  function downloadSelection() {
    const sel = selection;
    if (sel.length === 1 && !sel[0].is_dir)
      download('/api/files/download' + qs({ path: sel[0].path }));
    else postForm('/api/files/zip', { paths: sel.map((e) => e.path) });
  }

  async function runSearch(e?: Event) {
    e?.preventDefault();
    const q = query.trim();
    if (q.length < 2) {
      results = null;
      return;
    }
    searching = true;
    actionErr = '';
    try {
      const r = await get<{ results: Entry[]; truncated: boolean }>(
        '/api/search' + qs({ q, path })
      );
      results = r.results;
      truncated = r.truncated;
      selected.clear();
    } catch (x) {
      actionErr = errorMessage(x);
    } finally {
      searching = false;
    }
  }

  function clearSearch() {
    query = '';
    results = null;
    selected.clear();
  }

  function onDragEnter(e: DragEvent) {
    if (!writable || !e.dataTransfer?.types.includes('Files')) return;
    e.preventDefault();
    dragging++;
  }
  function onDragOver(e: DragEvent) {
    if (writable && e.dataTransfer?.types.includes('Files')) e.preventDefault();
  }
  function onDragLeave() {
    dragging = Math.max(0, dragging - 1);
  }
  async function onDrop(e: DragEvent) {
    dragging = 0;
    if (!writable || !e.dataTransfer) return;
    e.preventDefault();
    const files = await filesFromDrop(e.dataTransfer);
    if (files.length) uploader.add(files, path);
  }

  function pick(input: HTMLInputElement | undefined) {
    if (!input) return;
    const files = filesFromInput(input.files);
    if (files.length) uploader.add(files, path);
    input.value = '';
  }

  function iconFor(e: Entry): string {
    const k = kindOf(e);
    return k === 'dir' ? 'folder' : k === 'file' ? 'file' : k;
  }
</script>

<svelte:head><title>{path ? path.split('/').pop() : 'Files'}</title></svelte:head>

<div
  class="browser"
  role="region"
  aria-label="File browser"
  ondragenter={onDragEnter}
  ondragover={onDragOver}
  ondragleave={onDragLeave}
  ondrop={onDrop}
>
  <div class="toolbar">
    <nav class="crumbs" aria-label="Breadcrumbs">
      <a href="/files">Home</a>
      {#each crumbs as c, i (i)}
        <span class="muted">/</span>
        <a href={filesHref(crumbs.slice(0, i + 1).join('/'))}>{c}</a>
      {/each}
      {#if listing && listing.permission !== 'write'}
        <span class="badge">{listing.permission === 'read' ? 'read-only' : 'browse'}</span>
      {/if}
    </nav>
    <form class="search" onsubmit={runSearch}>
      <input type="search" placeholder="Search names…" bind:value={query} aria-label="Search" />
      <button class="icon" aria-label="Search" disabled={searching}
        ><Icon name="search" size={18} /></button
      >
    </form>
  </div>

  <div class="actions row">
    {#if selection.length}
      <span class="small"><strong>{selection.length}</strong> selected</span>
      <button class="small" onclick={downloadSelection}
        ><Icon name="download" size={16} />Download</button
      >
      {#if canModifySelection}
        {#if selection.length === 1}
          <button
            class="small"
            onclick={() => ((renameEntry = selection[0]), (newName = selection[0].name))}
          >
            <Icon name="edit" size={16} />Rename
          </button>
          <button class="small" onclick={() => (shareEntry = selection[0])}
            ><Icon name="share" size={16} />Share</button
          >
        {/if}
        <button class="small" onclick={() => (moveOpen = true)}
          ><Icon name="move" size={16} />Move</button
        >
        <button class="small danger" onclick={() => (confirmDelete = true)}
          ><Icon name="trash" size={16} />Delete</button
        >
      {/if}
      <button class="small ghost" onclick={() => selected.clear()}>Clear</button>
    {:else if writable && results === null}
      <button class="small primary" onclick={() => fileInput?.click()}
        ><Icon name="upload" size={16} />Upload</button
      >
      <button class="small hide-mobile" onclick={() => dirInput?.click()}
        ><Icon name="upload" size={16} />Upload folder</button
      >
      <button class="small" onclick={() => ((mkdirOpen = true), (newName = ''))}
        ><Icon name="newfolder" size={16} />New folder</button
      >
      {#if !listing?.grant_root || path}
        <button
          class="small"
          onclick={() =>
            (shareEntry = {
              name: crumbs[crumbs.length - 1] ?? 'Home',
              path,
              is_dir: true,
              size: 0,
              mtime: '',
            })}
          disabled={!path}
        >
          <Icon name="share" size={16} />Share folder
        </button>
      {/if}
    {/if}
    <span class="spacer"></span>
    <div class="seg">
      <button
        class="icon"
        class:on={view === 'list'}
        onclick={() => setView('list')}
        aria-label="List view"><Icon name="list" size={18} /></button
      >
      <button
        class="icon"
        class:on={view === 'grid'}
        onclick={() => setView('grid')}
        aria-label="Grid view"><Icon name="grid" size={18} /></button
      >
    </div>
  </div>
  <input type="file" multiple hidden bind:this={fileInput} onchange={() => pick(fileInput)} />
  <input type="file" multiple hidden bind:this={dirInput} onchange={() => pick(dirInput)} />

  {#if results !== null}
    <div class="notice row">
      <span
        >{results.length} result{results.length === 1 ? '' : 's'} for "{query}"{truncated
          ? ' (truncated)'
          : ''}</span
      >
      <span class="spacer"></span>
      <button class="small" onclick={clearSearch}>Clear search</button>
    </div>
  {/if}
  {#if actionErr}<div class="error">{actionErr}</div>{/if}

  {#if loading && !listing}
    <p class="muted pad">Loading…</p>
  {:else if err}
    <div class="error">{err}</div>
  {:else if entries.length === 0}
    <div class="empty muted">
      {#if results !== null}No matches.{:else if writable}This folder is empty. Drop files here to
        upload.{:else}This folder is empty.{/if}
    </div>
  {:else if view === 'list'}
    <div class="table-wrap">
      <table class="data files">
        <thead>
          <tr>
            <th class="cb"
              ><input
                type="checkbox"
                checked={selected.size > 0 && selected.size === entries.length}
                onchange={toggleAll}
                aria-label="Select all"
              /></th
            >
            <th
              ><button class="ghost small sort" onclick={() => setSort('name')}
                >Name {sortKey === 'name' ? (sortAsc ? '▲' : '▼') : ''}</button
              ></th
            >
            <th class="num hide-mobile"
              ><button class="ghost small sort" onclick={() => setSort('size')}
                >Size {sortKey === 'size' ? (sortAsc ? '▲' : '▼') : ''}</button
              ></th
            >
            <th class="hide-mobile"
              ><button class="ghost small sort" onclick={() => setSort('mtime')}
                >Modified {sortKey === 'mtime' ? (sortAsc ? '▲' : '▼') : ''}</button
              ></th
            >
          </tr>
        </thead>
        <tbody>
          {#each entries as e, i (e.path)}
            <tr class:sel={selected.has(e.path)}>
              <td class="cb"
                ><input
                  type="checkbox"
                  checked={selected.has(e.path)}
                  onclick={(ev) => toggle(e, i, ev)}
                  aria-label={'Select ' + e.name}
                /></td
              >
              <td>
                <button class="name ghost" onclick={() => openEntry(e, i)}>
                  <span class="ic" class:dir={e.is_dir}><Icon name={iconFor(e)} size={20} /></span>
                  <span class="label">
                    {e.name}
                    {#if results !== null}<span class="muted small path"
                        >{parentPath(e.path) || 'Home'}</span
                      >{/if}
                  </span>
                </button>
              </td>
              <td class="num muted hide-mobile">{e.is_dir ? '' : formatSize(e.size)}</td>
              <td class="muted hide-mobile">{formatDate(e.mtime)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {:else}
    <div class="sortbar row small">
      <span class="muted">Sort:</span>
      <button class="small ghost" class:on={sortKey === 'name'} onclick={() => setSort('name')}
        >Name</button
      >
      <button class="small ghost" class:on={sortKey === 'size'} onclick={() => setSort('size')}
        >Size</button
      >
      <button class="small ghost" class:on={sortKey === 'mtime'} onclick={() => setSort('mtime')}
        >Date</button
      >
      <span>{sortAsc ? '▲' : '▼'}</span>
    </div>
    <div class="grid">
      {#each entries as e, i (e.path)}
        <div class="tile" class:sel={selected.has(e.path)}>
          <input
            class="tcb"
            type="checkbox"
            checked={selected.has(e.path)}
            onclick={(ev) => toggle(e, i, ev)}
            aria-label={'Select ' + e.name}
          />
          <button class="ghost tbtn" onclick={() => openEntry(e, i)} title={e.name}>
            <div class="thumb">
              {#if thumbable(e) && !failedThumbs.has(e.path)}
                <img
                  loading="lazy"
                  alt=""
                  src={'/api/thumb' + qs({ path: e.path, v: e.mtime })}
                  onerror={() => failedThumbs.add(e.path)}
                />
              {:else}
                <span class="ic" class:dir={e.is_dir}><Icon name={iconFor(e)} size={48} /></span>
              {/if}
            </div>
            <div class="tname">{e.name}</div>
          </button>
        </div>
      {/each}
    </div>
  {/if}

  {#if dragging > 0}
    <div class="dropzone">
      <div>
        <Icon name="upload" size={48} />
        <p>Drop to upload here</p>
      </div>
    </div>
  {/if}
</div>

<UploadPanel {uploader} />

{#if previewIndex !== null}
  <Preview {entries} bind:index={previewIndex} onclose={() => (previewIndex = null)} />
{/if}

{#if mkdirOpen}
  <Modal title="New folder" onclose={() => (mkdirOpen = false)}>
    <form id="mkdir-form" onsubmit={mkdir}>
      <!-- svelte-ignore a11y_autofocus -->
      <input
        class="full"
        bind:value={newName}
        placeholder="Folder name"
        required
        maxlength="255"
        autofocus
      />
    </form>
    {#snippet footer()}
      <button onclick={() => (mkdirOpen = false)}>Cancel</button>
      <button class="primary" type="submit" form="mkdir-form">Create</button>
    {/snippet}
  </Modal>
{/if}

{#if renameEntry}
  <Modal title="Rename" onclose={() => (renameEntry = null)}>
    <form id="rename-form" onsubmit={rename}>
      <!-- svelte-ignore a11y_autofocus -->
      <input class="full" bind:value={newName} required maxlength="255" autofocus />
    </form>
    {#snippet footer()}
      <button onclick={() => (renameEntry = null)}>Cancel</button>
      <button class="primary" type="submit" form="rename-form">Rename</button>
    {/snippet}
  </Modal>
{/if}

{#if moveOpen}
  <FolderPicker
    title={`Move ${selection.length} item${selection.length === 1 ? '' : 's'} to…`}
    start={path}
    exclude={selection.filter((e) => e.is_dir).map((e) => e.path)}
    onpick={moveTo}
    onclose={() => (moveOpen = false)}
  />
{/if}

{#if shareEntry}
  <ShareDialog entry={shareEntry} onclose={() => (shareEntry = null)} />
{/if}

{#if confirmDelete}
  <Modal title="Move to trash?" onclose={() => (confirmDelete = false)}>
    <p>
      {selection.length === 1 ? `"${selection[0].name}"` : `${selection.length} items`} will be moved
      to the trash. You can restore them from the Trash page.
    </p>
    {#snippet footer()}
      <button onclick={() => (confirmDelete = false)}>Cancel</button>
      <button class="danger solid" onclick={remove}>Move to trash</button>
    {/snippet}
  </Modal>
{/if}

<style>
  .browser {
    max-width: 1200px;
    margin: 0 auto;
    padding: 1rem 1rem 6rem;
    min-height: calc(100vh - 60px);
    position: relative;
  }
  .toolbar {
    display: flex;
    gap: 1rem;
    align-items: center;
    flex-wrap: wrap;
    margin-bottom: 0.75rem;
  }
  .crumbs {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.3rem;
    flex: 1;
    font-size: 1.05rem;
    min-width: 0;
  }
  .crumbs a {
    color: var(--text);
    font-weight: 500;
  }
  .search {
    display: flex;
    align-items: center;
    gap: 0.2rem;
  }
  .search input {
    width: 220px;
  }
  .actions {
    margin-bottom: 0.75rem;
    min-height: 2.2rem;
  }
  .seg {
    display: flex;
    border: 1px solid var(--border);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .seg .on,
  .sortbar .on {
    background: var(--accent-soft);
    color: var(--accent);
  }
  .files td,
  .files th {
    padding-top: 0.25rem;
    padding-bottom: 0.25rem;
  }
  .cb {
    width: 2.2rem;
  }
  .num {
    text-align: right;
    white-space: nowrap;
  }
  th.num .sort {
    margin-left: auto;
  }
  .sort {
    padding: 0.2rem 0.3rem;
    font-weight: 600;
    color: var(--muted);
  }
  tr.sel td {
    background: var(--accent-soft);
  }
  .name {
    width: 100%;
    justify-content: flex-start;
    padding: 0.35rem 0.2rem;
    text-align: left;
    white-space: normal;
  }
  .name .label {
    display: flex;
    flex-direction: column;
    word-break: break-word;
  }
  .ic {
    color: var(--muted);
    display: inline-flex;
  }
  .ic.dir {
    color: #e0a526;
  }
  .empty {
    text-align: center;
    padding: 4rem 1rem;
    border: 2px dashed var(--border);
    border-radius: var(--radius);
  }
  .pad {
    padding: 1rem;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(150px, 1fr));
    gap: 0.75rem;
  }
  .sortbar {
    margin-bottom: 0.5rem;
  }
  .tile {
    position: relative;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .tile.sel {
    outline: 2px solid var(--accent);
  }
  .tcb {
    position: absolute;
    top: 0.4rem;
    left: 0.4rem;
    z-index: 2;
  }
  .tbtn {
    display: block;
    width: 100%;
    padding: 0;
    border: none;
    border-radius: 0;
    white-space: normal;
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
  .tname {
    padding: 0.4rem 0.5rem;
    font-size: 0.85rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    text-align: left;
  }
  .dropzone {
    position: fixed;
    inset: 0;
    background: rgba(37, 99, 235, 0.15);
    border: 3px dashed var(--accent);
    display: flex;
    align-items: center;
    justify-content: center;
    pointer-events: none;
    z-index: 45;
    color: var(--accent);
    text-align: center;
    font-weight: 600;
  }
  .full {
    width: 100%;
  }
  @media (max-width: 640px) {
    .search,
    .search input {
      width: 100%;
    }
    .grid {
      grid-template-columns: repeat(auto-fill, minmax(110px, 1fr));
    }
  }
</style>
