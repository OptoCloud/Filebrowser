<script lang="ts">
  import { SvelteSet } from 'svelte/reactivity';
  import { errorMessage, get, post } from '#lib/api.js';
  import { filesHref, formatDate, formatSize, parentPath } from '#lib/format.js';
  import type { TrashItem } from '#lib/types.js';
  import Icon from '#lib/components/Icon.svelte';
  import Modal from '#lib/components/Modal.svelte';

  let items = $state<TrashItem[]>([]);
  let retention = $state(0);
  let loaded = $state(false);
  let err = $state('');
  let notice = $state('');
  let confirm = $state<null | 'selected' | 'all'>(null);
  const selected = new SvelteSet<string>();

  async function load() {
    try {
      const r = await get<{ items: TrashItem[] | null; retention_days: number }>('/api/trash');
      items = r.items ?? [];
      retention = r.retention_days;
      for (const id of selected) if (!items.some((i) => i.id === id)) selected.delete(id);
    } catch (e) {
      err = errorMessage(e);
    } finally {
      loaded = true;
    }
  }

  $effect(() => {
    load();
  });

  async function restore(ids: string[]) {
    err = notice = '';
    try {
      const r = await post<{ paths: string[] | null }>('/api/trash/restore', { ids });
      const paths = r.paths ?? [];
      notice = paths.length === 1 ? `Restored to ${paths[0]}` : `Restored ${paths.length} items`;
      selected.clear();
    } catch (e) {
      err = errorMessage(e);
    }
    load();
  }

  async function purge() {
    const which = confirm;
    confirm = null;
    err = notice = '';
    try {
      if (which === 'all') await post('/api/trash/purge', { all: true });
      else await post('/api/trash/purge', { ids: [...selected] });
      selected.clear();
    } catch (e) {
      err = errorMessage(e);
    }
    load();
  }
</script>

<svelte:head><title>Trash</title></svelte:head>

<div class="page">
  <div class="row head">
    <h1>Trash</h1>
    <span class="spacer"></span>
    {#if selected.size}
      <button onclick={() => restore([...selected])}
        ><Icon name="restore" size={16} />Restore ({selected.size})</button
      >
      <button class="danger" onclick={() => (confirm = 'selected')}>Delete forever</button>
    {/if}
    <button class="danger" disabled={!items.length} onclick={() => (confirm = 'all')}
      >Empty trash</button
    >
  </div>
  {#if retention > 0}<p class="muted small">
      Items are deleted permanently after {retention} days.
    </p>{/if}
  {#if err}<div class="error">{err}</div>{/if}
  {#if notice}<div class="notice">{notice}</div>{/if}

  {#if !loaded}
    <p class="muted">Loading…</p>
  {:else if !items.length}
    <p class="muted">The trash is empty.</p>
  {:else}
    <div class="table-wrap">
      <table class="data">
        <thead>
          <tr>
            <th style="width:2rem">
              <input
                type="checkbox"
                aria-label="Select all"
                checked={selected.size === items.length}
                onchange={() =>
                  selected.size === items.length
                    ? selected.clear()
                    : items.forEach((i) => selected.add(i.id))}
              />
            </th>
            <th>Name</th>
            <th class="hide-mobile">Original location</th>
            <th class="hide-mobile">Size</th>
            <th>Deleted</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {#each items as it (it.id)}
            <tr>
              <td>
                <input
                  type="checkbox"
                  aria-label="Select"
                  checked={selected.has(it.id)}
                  onchange={() =>
                    selected.has(it.id) ? selected.delete(it.id) : selected.add(it.id)}
                />
              </td>
              <td>
                <span class="row"
                  ><Icon name={it.is_dir ? 'folder' : 'file'} size={18} />{it.original_path
                    .split('/')
                    .pop()}</span
                >
              </td>
              <td class="hide-mobile small"
                ><a href={filesHref(parentPath(it.original_path))}
                  >/{parentPath(it.original_path)}</a
                ></td
              >
              <td class="hide-mobile muted">{formatSize(it.size)}</td>
              <td class="muted small">{formatDate(it.trashed_at)}</td>
              <td><button class="small" onclick={() => restore([it.id])}>Restore</button></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

{#if confirm}
  <Modal title="Delete permanently?" onclose={() => (confirm = null)}>
    <p>
      {confirm === 'all' ? 'Everything in the trash' : `${selected.size} item(s)`} will be deleted permanently.
      This cannot be undone.
    </p>
    {#snippet footer()}
      <button onclick={() => (confirm = null)}>Cancel</button>
      <button class="danger solid" onclick={purge}>Delete forever</button>
    {/snippet}
  </Modal>
{/if}

<style>
  .head h1 {
    margin: 0;
  }
  .head {
    margin-bottom: 0.5rem;
  }
</style>
