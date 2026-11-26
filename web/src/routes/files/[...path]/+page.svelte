<script lang="ts">
  import { untrack } from 'svelte';
  import { SvelteSet } from 'svelte/reactivity';
  import { goto, invalidate } from '$app/navigation';
  import { page } from '$app/state';
  import { download, errorMessage, get, post, postForm, qs } from '#lib/api.js';
  import { formatDate, formatSize, parentPath, pathCrumbs } from '#lib/format.js';
  import { filesHref, previewHref } from '#lib/nav.js';
  import { PREF, loadPref, savePref } from '#lib/prefs.js';
  import { type SortKey, sortEntries } from '#lib/sort.js';
  import { LatestRequest } from '#lib/latest.js';
  import { isTyping, moveTarget, perRow } from '#lib/listkeys.js';
  import type { Entry } from '#lib/types.js';
  import type { PageProps } from './$types';
  import { appUploader, filesFromDrop, filesFromInput } from '#lib/upload.svelte.js';
  import ConfirmDialog from '#lib/components/ConfirmDialog.svelte';
  import EntryIcon from '#lib/components/EntryIcon.svelte';
  import EntryThumb from '#lib/components/EntryThumb.svelte';
  import FolderPicker from '#lib/components/FolderPicker.svelte';
  import Icon from '#lib/components/Icon.svelte';
  import Modal from '#lib/components/Modal.svelte';
  import Preview from '#lib/components/Preview.svelte';
  import ShareDialog from '#lib/components/ShareDialog.svelte';
  import VersionsDialog from '#lib/components/VersionsDialog.svelte';

  // The listing comes from the load function (+page.ts), so it is there
  // before the folder is shown.
  let { data }: PageProps = $props();
  const path = $derived(data.path);
  const listing = $derived(data.listing);
  const err = $derived(data.loadError);
  let actionErr = $state('');

  let view = $state<'list' | 'grid'>(loadPref(PREF.view, 'list') as 'list' | 'grid');
  let sortKey = $state<SortKey>(loadPref(PREF.sortKey, 'name') as SortKey);
  let sortAsc = $state(loadPref(PREF.sortAsc, '1') === '1');
  const selected = new SvelteSet<string>();
  let anchor = -1;

  let query = $state('');
  let results = $state<Entry[] | null>(null);
  let truncated = $state(false);
  let searching = $state(false);
  let searchingFor = '';

  // Path of the previewed file (an index would jump when the listing
  // changes). Opening one is a shallow navigation: it adds a history entry
  // with the file in its state and in the address, without leaving the
  // listing, so Back closes the preview and the address opens it again.
  // A history entry without that state is one that was really navigated to,
  // and its address says what is open.
  const previewPath = $derived(
    page.state.preview !== undefined ? page.state.preview : page.url.searchParams.get('preview')
  );
  // Whether the preview added a history entry of its own that closing can go back over.
  let previewPushed = false;
  let mkdirOpen = $state(false);
  let newName = $state('');
  let renameEntry = $state<Entry | null>(null);
  let moveOpen = $state(false);
  let shareEntry = $state<Entry | null>(null);
  let confirmDelete = $state(false);
  // Sizes of the folders here, by name, once asked for (the listing cannot say).
  let folderSizes = $state<Record<string, number> | null>(null);
  let sizesPartial = $state(false);
  // The file whose earlier versions (from snapshots) are shown.
  let versionsOf = $state<Entry | null>(null);
  let dragging = $state(0);

  let fileInput: HTMLInputElement | undefined = $state();
  let dirInput: HTMLInputElement | undefined = $state();
  let searchInput: HTMLInputElement | undefined = $state();
  let browser: HTMLElement | undefined = $state();

  const searchReq = new LatestRequest();

  const reload = () => invalidate('app:files');

  $effect(() => {
    void path;
    untrack(() => {
      selected.clear();
      anchor = -1;
      results = null;
      query = '';
      folderSizes = null;
    });
    // Leaving the folder drops its pending search.
    return () => {
      searchReq.abort();
      searching = false;
    };
  });

  // Uploads outlive this page (see appUploader); refresh when one lands here.
  $effect(() => {
    let reloadTimer: ReturnType<typeof setTimeout> | undefined;
    const off = appUploader.onFileDone((item) => {
      if (item.dir !== path) return;
      clearTimeout(reloadTimer);
      reloadTimer = setTimeout(reload, 400);
    });
    return () => {
      off();
      clearTimeout(reloadTimer);
    };
  });

  const writable = $derived(listing?.permission === 'write');
  const entries = $derived(sortEntries(results ?? listing?.entries ?? [], sortKey, sortAsc));
  const crumbs = $derived(pathCrumbs(path));
  const selection = $derived(entries.filter((e) => selected.has(e.path)));
  // In a traverse-only directory every child is a grant root (or leads to one).
  const canModify = $derived(results !== null || listing?.permission === 'write');
  const canModifySelection = $derived(selection.length > 0 && canModify);

  function setSort(k: SortKey) {
    if (sortKey === k) sortAsc = !sortAsc;
    else {
      sortKey = k;
      sortAsc = k === 'name';
    }
    savePref(PREF.sortKey, sortKey);
    savePref(PREF.sortAsc, sortAsc ? '1' : '0');
  }

  const ariaSort = (k: SortKey) =>
    sortKey !== k ? undefined : sortAsc ? ('ascending' as const) : ('descending' as const);

  function setView(v: 'list' | 'grid') {
    view = v;
    savePref(PREF.view, v);
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

  const entryHref = (e: Entry) => (e.is_dir ? filesHref(e.path) : previewHref(path, e.path));

  function showPreview(p: string | null, replace = false) {
    if (p === null) return closePreview();
    if (!replace) previewPushed = true;
    void goto(previewHref(path, p), { replace, shallow: true, state: { preview: p } });
  }

  function closePreview() {
    if (previewPath === null) return;
    if (previewPushed) {
      previewPushed = false;
      history.back();
    } else void goto(filesHref(path), { replace: true, shallow: true, state: { preview: null } });
  }

  // Entries are links, so they open in a new tab, copy and preload like any
  // other; a plain click on a file opens its preview over the listing.
  function onEntryClick(e: Entry, ev: MouseEvent) {
    if (e.is_dir || ev.button !== 0 || ev.ctrlKey || ev.metaKey || ev.shiftKey || ev.altKey) return;
    ev.preventDefault();
    showPreview(e.path);
  }

  // Keyboard: arrows move between entries, Enter opens (they are links),
  // Space selects, Delete and F2 act, Ctrl+A selects all, / searches.
  function onkeydown(ev: KeyboardEvent) {
    const t = ev.target as HTMLElement | null;
    if (ev.defaultPrevented || !t || !browser || document.querySelector('dialog[open]')) return;
    if (isTyping(t)) return;
    const els = Array.from(browser.querySelectorAll<HTMLElement>('[data-entry]'));
    const at = els.indexOf(document.activeElement as HTMLElement);
    const plain = !(ev.ctrlKey || ev.metaKey || ev.altKey);
    const move = plain
      ? moveTarget(ev.key, at, els.length, view === 'grid' ? perRow(els) : 1)
      : null;
    let handled = true;
    if (ev.key === '/') searchInput?.focus();
    else if ((ev.ctrlKey || ev.metaKey) && ev.key.toLowerCase() === 'a')
      entries.forEach((e) => selected.add(e.path));
    else if (!plain) handled = false;
    else if (move !== null) els[move]?.focus();
    else if (ev.key === ' ' && at >= 0) toggle(entries[at], at, ev);
    else if (ev.key === 'Escape' && selected.size) selected.clear();
    else if ((ev.key === 'Delete' || ev.key === 'F2') && canModify) {
      // On the selection, or else on the entry the keyboard is at.
      if (!selected.size && at >= 0) selected.add(entries[at].path);
      if (ev.key === 'Delete' && selected.size) confirmDelete = true;
      else if (ev.key === 'F2' && selected.size === 1) {
        renameEntry = entries.find((e) => selected.has(e.path)) ?? null;
        newName = renameEntry?.name ?? '';
      } else handled = false;
    } else handled = false;
    if (handled) ev.preventDefault();
  }

  async function act(fn: () => Promise<unknown>) {
    actionErr = '';
    try {
      await fn();
    } catch (e) {
      actionErr = errorMessage(e);
    }
    await reload();
    if (results !== null) await runSearch();
  }

  async function loadFolderSizes() {
    actionErr = '';
    try {
      const r = await get<{ sizes: Record<string, number>; partial: boolean }>(
        '/api/files/sizes' + qs({ path })
      );
      folderSizes = r.sizes;
      sizesPartial = r.partial;
    } catch (e) {
      actionErr = errorMessage(e);
    }
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
      searchReq.abort();
      searching = false;
      results = null;
      return;
    }
    // Submitted again for the same query: the running search will answer.
    if (e && searching && q === searchingFor) return;
    const signal = searchReq.next();
    searching = true;
    searchingFor = q;
    actionErr = '';
    try {
      const r = await get<{ results: Entry[]; truncated: boolean }>(
        '/api/search' + qs({ q, path }),
        { signal }
      );
      if (signal.aborted) return;
      results = r.results;
      truncated = r.truncated;
      selected.clear();
    } catch (x) {
      if (signal.aborted) return;
      actionErr = errorMessage(x);
    }
    searching = false;
  }

  function clearSearch() {
    searchReq.abort();
    searching = false;
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
    if (files.length) appUploader.add(files, path);
  }

  function pick(input: HTMLInputElement | undefined) {
    if (!input) return;
    const files = filesFromInput(input.files);
    if (files.length) appUploader.add(files, path);
    input.value = '';
  }
</script>

<svelte:head><title>{crumbs.at(-1)?.name ?? 'Files'}</title></svelte:head>

<svelte:window {onkeydown} />

<div
  class="browser"
  role="region"
  aria-label="File browser"
  bind:this={browser}
  ondragenter={onDragEnter}
  ondragover={onDragOver}
  ondragleave={onDragLeave}
  ondrop={onDrop}
>
  <div class="toolbar">
    <nav class="crumbs" aria-label="Breadcrumbs">
      <a href={filesHref('')}>Home</a>
      {#each crumbs as c (c.path)}
        <span class="muted">/</span>
        <a href={filesHref(c.path)}>{c.name}</a>
      {/each}
      {#if listing?.permission === 'read'}<span class="badge">read-only</span>{/if}
    </nav>
    <form class="search" onsubmit={runSearch}>
      <input
        type="search"
        placeholder="Search names…"
        bind:value={query}
        bind:this={searchInput}
        aria-label="Search"
        aria-keyshortcuts="/"
      />
      <button class="icon" aria-label="Search" disabled={searching}>
        <Icon name="search" size={18} />
      </button>
    </form>
  </div>

  <div class="actions row">
    {#if selection.length}
      <span class="small"><strong>{selection.length}</strong> selected</span>
      <button class="small" onclick={downloadSelection}>
        <Icon name="download" size={16} />Download
      </button>
      {#if selection.length === 1 && !selection[0].is_dir && page.data.me?.snapshots}
        <button class="small" onclick={() => (versionsOf = selection[0])}>Versions</button>
      {/if}
      {#if canModifySelection}
        {#if selection.length === 1}
          <button
            class="small"
            onclick={() => ((renameEntry = selection[0]), (newName = selection[0].name))}
          >
            <Icon name="edit" size={16} />Rename
          </button>
          <button class="small" onclick={() => (shareEntry = selection[0])}>
            <Icon name="share" size={16} />Share
          </button>
        {/if}
        <button class="small" onclick={() => (moveOpen = true)}>
          <Icon name="move" size={16} />Move
        </button>
        <button class="small danger" onclick={() => (confirmDelete = true)}>
          <Icon name="trash" size={16} />Delete
        </button>
      {/if}
      <button class="small ghost" onclick={() => selected.clear()}>Clear</button>
    {:else if writable && results === null}
      <button class="small primary" onclick={() => fileInput?.click()}>
        <Icon name="upload" size={16} />Upload
      </button>
      <button class="small hide-mobile" onclick={() => dirInput?.click()}>
        <Icon name="upload" size={16} />Upload folder
      </button>
      <button class="small" onclick={() => ((mkdirOpen = true), (newName = ''))}>
        <Icon name="newfolder" size={16} />New folder
      </button>
      {#if !listing?.grant_root || path}
        <button
          class="small"
          onclick={() =>
            (shareEntry = {
              name: crumbs.at(-1)?.name ?? 'Home',
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
    {#if !selection.length && results === null && entries.some((e) => e.is_dir)}
      <button
        class="small"
        onclick={loadFolderSizes}
        title="Adds up what each folder holds; large folders take a while"
      >
        {folderSizes && sizesPartial ? 'Keep counting' : 'Folder sizes'}
      </button>
    {/if}
    <span class="spacer"></span>
    <div class="seg">
      <button
        class="icon"
        class:on={view === 'list'}
        onclick={() => setView('list')}
        aria-label="List view"
        aria-pressed={view === 'list'}
      >
        <Icon name="list" size={18} />
      </button>
      <button
        class="icon"
        class:on={view === 'grid'}
        onclick={() => setView('grid')}
        aria-label="Grid view"
        aria-pressed={view === 'grid'}
      >
        <Icon name="grid" size={18} />
      </button>
    </div>
  </div>
  <input type="file" multiple hidden bind:this={fileInput} onchange={() => pick(fileInput)} />
  <input
    type="file"
    multiple
    webkitdirectory
    hidden
    bind:this={dirInput}
    onchange={() => pick(dirInput)}
  />

  {#if results !== null}
    <div class="notice row" role="status">
      <span>
        {results.length} result{results.length === 1 ? '' : 's'} for "{query}"{truncated
          ? ' (truncated)'
          : ''}
      </span>
      <span class="spacer"></span>
      <button class="small" onclick={clearSearch}>Clear search</button>
    </div>
  {/if}
  {#if actionErr}<div class="error" role="alert">{actionErr}</div>{/if}

  {#if err}
    <div class="error" role="alert">{err}</div>
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
            <th class="cb">
              <input
                type="checkbox"
                checked={selected.size > 0 && selected.size === entries.length}
                onchange={toggleAll}
                aria-label="Select all"
              />
            </th>
            <th aria-sort={ariaSort('name')}>
              <button class="ghost small sort" onclick={() => setSort('name')}>
                Name {sortKey === 'name' ? (sortAsc ? '▲' : '▼') : ''}
              </button>
            </th>
            <th class="num hide-mobile" aria-sort={ariaSort('size')}>
              <button class="ghost small sort" onclick={() => setSort('size')}>
                Size {sortKey === 'size' ? (sortAsc ? '▲' : '▼') : ''}
              </button>
            </th>
            <th class="hide-mobile" aria-sort={ariaSort('mtime')}>
              <button class="ghost small sort" onclick={() => setSort('mtime')}>
                Modified {sortKey === 'mtime' ? (sortAsc ? '▲' : '▼') : ''}
              </button>
            </th>
          </tr>
        </thead>
        <tbody>
          {#each entries as e, i (e.path)}
            <tr class:sel={selected.has(e.path)}>
              <td class="cb">
                <input
                  type="checkbox"
                  checked={selected.has(e.path)}
                  onclick={(ev) => toggle(e, i, ev)}
                  aria-label={'Select ' + e.name}
                />
              </td>
              <td>
                <a
                  class="name"
                  href={entryHref(e)}
                  data-entry
                  data-sveltekit-reset={e.is_dir ? undefined : 'false'}
                  onclick={(ev) => onEntryClick(e, ev)}
                >
                  <EntryIcon entry={e} />
                  <span class="label">
                    {e.name}
                    {#if results !== null}<span class="muted small path">
                        {parentPath(e.path) || 'Home'}
                      </span>{/if}
                  </span>
                </a>
              </td>
              <td class="num muted hide-mobile">
                {e.is_dir
                  ? folderSizes && results === null && folderSizes[e.name] !== undefined
                    ? formatSize(folderSizes[e.name])
                    : ''
                  : formatSize(e.size)}
              </td>
              <td class="muted hide-mobile">{formatDate(e.mtime)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {:else}
    <div class="sortbar row small">
      <span class="muted">Sort:</span>
      <button class="small ghost" class:on={sortKey === 'name'} onclick={() => setSort('name')}>
        Name
      </button>
      <button class="small ghost" class:on={sortKey === 'size'} onclick={() => setSort('size')}>
        Size
      </button>
      <button class="small ghost" class:on={sortKey === 'mtime'} onclick={() => setSort('mtime')}>
        Date
      </button>
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
          <a
            class="tbtn"
            href={entryHref(e)}
            title={e.name}
            data-entry
            data-sveltekit-reset={e.is_dir ? undefined : 'false'}
            onclick={(ev) => onEntryClick(e, ev)}
          >
            <EntryThumb entry={e} src={'/api/thumb' + qs({ path: e.path, v: e.mtime })} />
            <div class="tname">{e.name}</div>
          </a>
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

{#if previewPath !== null}
  <Preview
    {entries}
    bind:path={() => previewPath, (p) => showPreview(p, true)}
    onclose={closePreview}
  />
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
    title="Move {selection.length} item{selection.length === 1 ? '' : 's'} to…"
    start={path}
    exclude={selection.filter((e) => e.is_dir).map((e) => e.path)}
    onpick={moveTo}
    onclose={() => (moveOpen = false)}
  />
{/if}

{#if versionsOf}
  <VersionsDialog entry={versionsOf} onclose={() => (versionsOf = null)} />
{/if}

{#if shareEntry}
  <ShareDialog entry={shareEntry} onclose={() => (shareEntry = null)} />
{/if}

{#if confirmDelete}
  <ConfirmDialog
    title="Move to trash?"
    confirmLabel="Move to trash"
    onconfirm={remove}
    onclose={() => (confirmDelete = false)}
  >
    <p>
      {selection.length === 1 ? `"${selection[0].name}"` : `${selection.length} items`} will be moved
      to the trash. You can restore {selection.length === 1 ? 'it' : 'them'} from the Trash page.
    </p>
  </ConfirmDialog>
{/if}

<style>
  .browser {
    max-width: 1200px;
    margin: 0 auto;
    padding: 1rem 1rem 6rem;
    min-height: calc(100dvh - 60px);
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
    display: flex;
    align-items: center;
    gap: 0.5rem;
    padding: 0.35rem 0.2rem;
    border-radius: var(--radius);
    color: var(--text);
    text-decoration: none;
  }
  .name:hover {
    background: var(--surface-2);
  }
  .name .label {
    display: flex;
    flex-direction: column;
    word-break: break-word;
  }
  .empty {
    text-align: center;
    padding: 4rem 1rem;
    border: 2px dashed var(--border);
    border-radius: var(--radius);
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
    color: var(--text);
    text-decoration: none;
  }
  .tbtn:focus-visible {
    outline-offset: -2px;
  }
  /* Rows far below the fold are not laid out until scrolled near. */
  .files tbody tr {
    content-visibility: auto;
    contain-intrinsic-size: auto 2.6rem;
  }
  .tile {
    content-visibility: auto;
    contain-intrinsic-size: auto 190px;
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
