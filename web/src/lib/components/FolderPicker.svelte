<script lang="ts">
  import { errorMessage, get, qs } from '#lib/api.js';
  import { baseName, parentPath, pathCrumbs } from '#lib/format.js';
  import { LatestRequest } from '#lib/latest.js';
  import type { Listing } from '#lib/types.js';
  import Icon from './Icon.svelte';
  import Modal from './Modal.svelte';

  let {
    title = 'Choose a folder',
    start = '',
    exclude = [],
    onpick,
    onclose,
  }: {
    title?: string;
    start?: string;
    exclude?: string[];
    onpick: (dir: string) => void;
    onclose: () => void;
  } = $props();

  let dir = $state('');
  let listing = $state<Listing | null>(null);
  let err = $state('');
  const req = new LatestRequest();

  // Falls back to Home when p cannot be opened, keeping that error visible.
  async function open(p: string, fallbackErr = '') {
    const signal = req.next();
    try {
      const l = await get<Listing>('/api/files' + qs({ path: p }), { signal });
      if (signal.aborted) return;
      listing = l;
      dir = p;
      err = fallbackErr;
    } catch (e) {
      if (signal.aborted) return;
      if (p !== '') open('', errorMessage(e));
      else err = errorMessage(e);
    }
  }

  $effect(() => {
    open(start);
    return () => req.abort();
  });

  const dirs = $derived(
    listing ? listing.entries.filter((e) => e.is_dir && !exclude.includes(e.path)) : []
  );
  const canPick = $derived(
    listing?.permission === 'write' && !exclude.some((x) => dir === x || dir.startsWith(x + '/'))
  );
</script>

<Modal {title} {onclose}>
  <div class="crumbs small">
    <button class="small ghost" onclick={() => open('')}>Home</button>
    {#each pathCrumbs(dir) as c (c.path)}
      <span class="muted">/</span>
      <button class="small ghost" onclick={() => open(c.path)}>{c.name}</button>
    {/each}
  </div>
  {#if err}<div class="error">{err}</div>{/if}
  <ul>
    {#if dir}
      <li>
        <button class="ghost" onclick={() => open(parentPath(dir))}
          ><Icon name="up" size={18} />..</button
        >
      </li>
    {/if}
    {#each dirs as d (d.path)}
      <li>
        <button class="ghost" onclick={() => open(d.path)}
          ><Icon name="folder" size={18} />{d.name}</button
        >
      </li>
    {:else}
      <li class="muted small empty">No subfolders</li>
    {/each}
  </ul>
  {#snippet footer()}
    <span class="muted small spacer"
      >{dir ? baseName(dir) : 'Home'}{canPick ? '' : ' (not writable)'}</span
    >
    <button onclick={onclose}>Cancel</button>
    <button class="primary" disabled={!canPick} onclick={() => onpick(dir)}
      >Choose this folder</button
    >
  {/snippet}
</Modal>

<style>
  .crumbs {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 0.1rem;
    margin-bottom: 0.5rem;
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    max-height: 50vh;
    overflow-y: auto;
    border: 1px solid var(--border);
    border-radius: var(--radius);
  }
  li button {
    width: 100%;
    justify-content: flex-start;
    border-radius: 0;
  }
  .empty {
    padding: 0.6rem;
  }
</style>
