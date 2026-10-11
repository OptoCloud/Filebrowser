<script lang="ts">
  import { errorMessage, get, qs } from '#lib/api.js';
  import { baseName, parentPath } from '#lib/format.js';
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

  async function open(p: string) {
    err = '';
    try {
      listing = await get<Listing>('/api/files' + qs({ path: p }));
      dir = p;
    } catch (e) {
      err = errorMessage(e);
      if (p !== '') open('');
    }
  }

  $effect(() => {
    open(start);
  });

  const dirs = $derived(
    listing ? listing.entries.filter((e) => e.is_dir && !exclude.includes(e.path)) : []
  );
  const crumbs = $derived(dir ? dir.split('/') : []);
  const canPick = $derived(
    listing?.permission === 'write' && !exclude.some((x) => dir === x || dir.startsWith(x + '/'))
  );
</script>

<Modal {title} {onclose}>
  <div class="crumbs small">
    <button class="small ghost" onclick={() => open('')}>Home</button>
    {#each crumbs as c, i (i)}
      <span class="muted">/</span>
      <button class="small ghost" onclick={() => open(crumbs.slice(0, i + 1).join('/'))}>{c}</button
      >
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
