<script lang="ts">
  import { errorMessage, post } from '#lib/api.js';
  import { formatDate } from '#lib/format.js';
  import type { Entry, Share } from '#lib/types.js';
  import CopyField from './CopyField.svelte';
  import Modal from './Modal.svelte';

  let { entry, onclose }: { entry: Entry; onclose: () => void } = $props();

  let kind = $state<'download' | 'upload'>('download');
  let expiresIn = $state(7 * 86400);
  let password = $state('');
  let maxDownloads = $state(0);
  let busy = $state(false);
  let err = $state('');
  let result = $state<{ share: Share; url: string } | null>(null);

  const expiries = [
    { label: '1 hour', value: 3600 },
    { label: '1 day', value: 86400 },
    { label: '7 days', value: 7 * 86400 },
    { label: '30 days', value: 30 * 86400 },
    { label: '90 days', value: 90 * 86400 },
  ];

  async function create(e: Event) {
    e.preventDefault();
    busy = true;
    err = '';
    try {
      result = await post<{ share: Share; url: string }>('/api/shares', {
        path: entry.path,
        kind,
        expires_in: expiresIn,
        password,
        max_downloads: kind === 'download' ? maxDownloads || 0 : 0,
      });
    } catch (x) {
      err = errorMessage(x);
    } finally {
      busy = false;
    }
  }
</script>

<Modal title={`Share "${entry.name}"`} {onclose}>
  {#if result}
    <p>
      Anyone with this link can {result.share.kind === 'upload'
        ? 'upload files to this folder'
        : 'download'} until
      {formatDate(result.share.expires_at)}.
    </p>
    <CopyField value={result.url} />
    <p class="warn small">
      Copy the link now. For security it is stored hashed and cannot be shown again.
    </p>
  {:else}
    <form id="share-form" onsubmit={create}>
      {#if entry.is_dir}
        <div class="field-row">
          <label class="inline"
            ><input type="radio" bind:group={kind} value="download" /> Download link</label
          >
          <label class="inline"
            ><input type="radio" bind:group={kind} value="upload" /> Upload-only (drop) link</label
          >
        </div>
      {/if}
      <label class="field">
        Expires after
        <select bind:value={expiresIn}>
          {#each expiries as x (x.value)}<option value={x.value}>{x.label}</option>{/each}
        </select>
      </label>
      <label class="field">
        Password (optional)
        <input
          type="password"
          bind:value={password}
          autocomplete="new-password"
          minlength="8"
          maxlength="256"
          placeholder="at least 8 characters"
        />
      </label>
      {#if kind === 'download'}
        <label class="field">
          Maximum downloads (0 = unlimited)
          <input type="number" min="0" bind:value={maxDownloads} />
        </label>
      {/if}
      {#if err}<div class="error">{err}</div>{/if}
    </form>
  {/if}
  {#snippet footer()}
    {#if result}
      <button class="primary" onclick={onclose}>Done</button>
    {:else}
      <button onclick={onclose}>Cancel</button>
      <button class="primary" type="submit" form="share-form" disabled={busy}>Create link</button>
    {/if}
  {/snippet}
</Modal>

<style>
  .field-row {
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
    margin-bottom: 0.8rem;
  }
</style>
