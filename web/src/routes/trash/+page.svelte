<script lang="ts">
  import { SvelteSet } from 'svelte/reactivity';
  import { invalidate } from '$app/navigation';
  import { errorMessage, post } from '#lib/api.js';
  import { baseName, filesHref, formatDate, formatSize, parentPath } from '#lib/format.js';
  import ConfirmDialog from '#lib/components/ConfirmDialog.svelte';
  import Icon from '#lib/components/Icon.svelte';

  let { data } = $props();

  // Loaded by +page.ts; reloaded with invalidate('app:trash').
  const items = $derived(data.items);
  const retention = $derived(data.retention);
  let actionErr = $state('');
  const err = $derived(actionErr || data.loadError);
  let notice = $state('');
  let confirm = $state<null | 'selected' | 'all'>(null);
  const selected = new SvelteSet<string>();

  async function reload() {
    await invalidate('app:trash');
    for (const id of selected) if (!items.some((i) => i.id === id)) selected.delete(id);
  }

  async function restore(ids: string[]) {
    actionErr = notice = '';
    try {
      const r = await post<{ paths: string[] | null }>('/api/trash/restore', { ids });
      const paths = r.paths ?? [];
      notice = paths.length === 1 ? `Restored to ${paths[0]}` : `Restored ${paths.length} items`;
      selected.clear();
    } catch (e) {
      actionErr = errorMessage(e);
    }
    reload();
  }

  async function purge() {
    const which = confirm;
    confirm = null;
    actionErr = notice = '';
    try {
      if (which === 'all') await post('/api/trash/purge', { all: true });
      else await post('/api/trash/purge', { ids: [...selected] });
      selected.clear();
    } catch (e) {
      actionErr = errorMessage(e);
    }
    reload();
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

  {#if !items.length}
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
                  ><Icon name={it.is_dir ? 'folder' : 'file'} size={18} />{baseName(
                    it.original_path
                  )}</span
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
  <ConfirmDialog
    title="Delete permanently?"
    confirmLabel="Delete forever"
    onconfirm={purge}
    onclose={() => (confirm = null)}
  >
    <p>
      {confirm === 'all' ? 'Everything in the trash' : `${selected.size} item(s)`} will be deleted permanently.
      This cannot be undone.
    </p>
  </ConfirmDialog>
{/if}

<style>
  .head h1 {
    margin: 0;
  }
  .head {
    margin-bottom: 0.5rem;
  }
</style>
