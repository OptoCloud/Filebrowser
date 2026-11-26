<script lang="ts">
  import { errorMessage, get, qs } from '#lib/api.js';
  import { formatDate, formatSize } from '#lib/format.js';
  import type { Entry } from '#lib/types.js';
  import Modal from './Modal.svelte';

  // The earlier versions of a file that the server's snapshots hold.
  let { entry, onclose }: { entry: Entry; onclose: () => void } = $props();

  interface Version {
    snapshot: string;
    size: number;
    mtime: string;
  }
  let versions = $state<Version[] | null>(null);
  let err = $state('');

  $effect(() => {
    const path = entry.path;
    versions = null;
    err = '';
    get<{ versions: Version[] }>('/api/files/versions' + qs({ path }))
      .then((r) => (versions = r.versions))
      .catch((e) => (err = errorMessage(e)));
  });
</script>

<Modal title="Earlier versions of “{entry.name}”" {onclose}>
  {#if err}
    <div class="error" role="alert">{err}</div>
  {:else if versions === null}
    <p class="muted">Looking through the snapshots…</p>
  {:else if versions.length === 0}
    <p class="muted">The snapshots hold no other version of this file.</p>
  {:else}
    <ul class="plain">
      {#each versions as v (v.snapshot)}
        <li class="row small">
          <span>
            {formatDate(v.mtime)} · {formatSize(v.size)}
            <br /><span class="muted">snapshot {v.snapshot}</span>
          </span>
          <span class="spacer"></span>
          <a
            class="btn small"
            href={'/api/files/version' + qs({ path: entry.path, snapshot: v.snapshot })}
            rel="external"
            download
          >
            Download
          </a>
        </li>
      {/each}
    </ul>
  {/if}
  {#snippet footer()}
    <button class="primary" onclick={onclose}>Close</button>
  {/snippet}
</Modal>
