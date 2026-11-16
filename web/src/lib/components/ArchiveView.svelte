<script lang="ts">
  import { errorMessage, get, qs } from '#lib/api.js';
  import { formatDate, formatSize } from '#lib/format.js';
  import type { ArchiveIndex } from '#lib/types.js';
  import Icon from './Icon.svelte';

  // The index of an archive: what it holds, and whether a password is needed.
  let { path, summary }: { path: string; summary: ArchiveIndex } = $props();

  let index = $state<ArchiveIndex | null>(null);
  let err = $state('');

  $effect(() => {
    const p = path;
    index = null;
    err = '';
    if (!summary.listable || summary.encrypted === 'headers') return;
    const ctrl = new AbortController();
    get<ArchiveIndex>('/api/files/archive' + qs({ path: p }), { signal: ctrl.signal })
      .then((r) => (index = r))
      .catch((x) => {
        if (!ctrl.signal.aborted) err = errorMessage(x);
      });
    return () => ctrl.abort();
  });

  const a = $derived(index ?? summary);
  // A Unity package is listed by the project paths of its assets.
  const unity = $derived(a.format === 'unitypackage');
  const protection = $derived(
    {
      headers: 'Password protected: even the list of files is encrypted.',
      entries: 'Password protected: the files inside need a password to extract.',
      unknown: '',
      no: '',
    }[a.encrypted]
  );
</script>

<div class="archive">
  <div class="bar">
    <strong>{unity ? 'Unity package' : a.format.toUpperCase() + ' archive'}</strong>
    {#if index}
      <span class="muted small"
        >{index.truncated ? `first ${index.entries?.length ?? 0} of ` : ''}{index.count}
        {unity
          ? index.count === 1
            ? 'asset'
            : 'assets'
          : index.count === 1
            ? 'entry'
            : 'entries'}{index.truncated && index.count <= (index.entries?.length ?? 0)
          ? ' or more'
          : ''}</span
      >
    {/if}
  </div>
  {#if protection}
    <p class="notice lock"><Icon name="lock" size={16} /> {protection}</p>
  {/if}
  {#if err}
    <div class="error">{err}</div>
  {:else if !a.listable || a.encrypted === 'headers'}
    <p class="muted pad">
      {a.encrypted === 'headers'
        ? 'Its contents cannot be listed without the password.'
        : 'The contents of this kind of archive cannot be listed here. Download it to open it.'}
    </p>
  {:else if !index}
    <p class="muted pad">Reading the archive…</p>
  {:else if !index.entries?.length}
    <p class="muted pad">This archive is empty.</p>
  {:else}
    <div class="scroll">
      <table>
        <thead
          ><tr><th>Name</th><th class="num">Size</th><th class="hide-mobile">Modified</th></tr
          ></thead
        >
        <tbody>
          {#each index.entries as e, i (i)}
            <tr>
              <td class="name">
                <Icon name={e.is_dir ? 'folder' : 'file'} size={16} />
                <span>{e.name}</span>
                {#if e.encrypted}<Icon name="lock" size={14} title="Password protected" />{/if}
              </td>
              <td class="num">{e.is_dir ? '' : formatSize(e.size)}</td>
              <td class="hide-mobile muted">{formatDate(e.mtime)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

<style>
  .archive {
    width: 100%;
    max-width: 1000px;
    height: 100%;
    display: flex;
    flex-direction: column;
    background: var(--surface);
    color: var(--text);
    border-radius: var(--radius);
    overflow: hidden;
  }
  .bar {
    display: flex;
    gap: 0.75rem;
    align-items: baseline;
    padding: 0.5rem 0.75rem;
    border-bottom: 1px solid var(--border);
  }
  .lock {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    margin: 0.5rem 0.75rem 0;
  }
  .pad {
    padding: 0.75rem;
  }
  .scroll {
    flex: 1;
    min-height: 0;
    overflow: auto;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 0.88rem;
  }
  th {
    position: sticky;
    top: 0;
    background: var(--surface-2);
    text-align: left;
    font-weight: 600;
  }
  th,
  td {
    padding: 0.3rem 0.75rem;
  }
  tbody tr:nth-child(even) {
    background: color-mix(in srgb, var(--surface-2) 50%, transparent);
  }
  .name {
    display: flex;
    align-items: center;
    gap: 0.4rem;
    word-break: break-all;
  }
  .num {
    text-align: right;
    white-space: nowrap;
  }
</style>
