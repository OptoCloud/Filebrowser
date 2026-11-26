<script lang="ts">
  import { copyText, errorMessage, post } from '#lib/api.js';
  import { formatDate } from '#lib/format.js';
  import type { Entry, Share } from '#lib/types.js';
  import CopyField from './CopyField.svelte';
  import Modal from './Modal.svelte';

  let { entry, onclose }: { entry: Entry; onclose: () => void } = $props();

  let kind = $state<'download' | 'upload'>('download');
  let expiresIn = $state(7 * 86400);
  let password = $state('');
  let maxDownloads = $state<number | null>(null);
  // Drop link limits; empty leaves the server default.
  let maxTotalGB = $state<number | null>(null);
  let maxFiles = $state<number | null>(null);
  let maxFileGB = $state<number | null>(null);
  let types = $state<string[]>([]);
  let singleSession = $state(false);
  const GB = 1024 ** 3;
  const kinds = [
    { value: 'image', label: 'Images' },
    { value: 'video', label: 'Video' },
    { value: 'audio', label: 'Audio' },
    { value: 'document', label: 'Documents' },
    { value: 'archive', label: 'Archives' },
  ];
  let busy = $state(false);
  let err = $state('');
  let result = $state<{ share: Share; url: string } | null>(null);
  // The link cannot be shown again, so it goes to the clipboard right away.
  let copied = $state(false);

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
        ...(kind === 'upload' && {
          max_bytes: Math.round((maxTotalGB || 0) * GB),
          max_files: maxFiles || 0,
          max_file_bytes: Math.round((maxFileGB || 0) * GB),
          allowed_types: types,
          single_session: singleSession,
        }),
      });
      copied = await copyText(result.url);
    } catch (x) {
      err = errorMessage(x);
    } finally {
      busy = false;
    }
  }
</script>

<Modal title="Share “{entry.name}”" {onclose}>
  {#if result}
    <p>
      Anyone with this link can {result.share.kind === 'upload'
        ? 'upload files to this folder'
        : 'download'} until
      {formatDate(result.share.expires_at)}.
    </p>
    <CopyField value={result.url} />
    {#if copied}
      <p class="notice small">Link copied to the clipboard. It cannot be shown again.</p>
    {:else}
      <p class="warn small">
        Copy the link now. For security it is stored hashed and cannot be shown again.
      </p>
    {/if}
  {:else}
    <form id="share-form" onsubmit={create}>
      {#if entry.is_dir}
        <div class="field-row">
          <label class="inline">
            <input type="radio" bind:group={kind} value="download" /> Download link
          </label>
          <label class="inline">
            <input type="radio" bind:group={kind} value="upload" /> Upload-only (drop) link
          </label>
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
          minlength="10"
          maxlength="256"
          placeholder="at least 10 characters"
        />
      </label>
      {#if kind === 'download'}
        <label class="field">
          Maximum downloads
          <input type="number" min="1" placeholder="Unlimited" bind:value={maxDownloads} />
        </label>
      {:else}
        <label class="field">
          Total size limit (GB)
          <input
            type="number"
            min="0.001"
            step="any"
            placeholder="Server default"
            bind:value={maxTotalGB}
          />
        </label>
        <label class="field">
          Maximum number of files
          <input type="number" min="1" placeholder="Server default" bind:value={maxFiles} />
        </label>
        <label class="field">
          Largest single file (GB)
          <input
            type="number"
            min="0.001"
            step="any"
            placeholder="Server default"
            bind:value={maxFileGB}
          />
        </label>
        <fieldset class="field-row">
          <legend>Accept only (nothing ticked accepts any file)</legend>
          {#each kinds as k (k.value)}
            <label class="inline">
              <input type="checkbox" bind:group={types} value={k.value} />
              {k.label}
            </label>
          {/each}
        </fieldset>
        <label class="inline">
          <input type="checkbox" bind:checked={singleSession} />
          Only the first browser to open the link may use it
        </label>
      {/if}
      {#if err}<div class="error" role="alert">{err}</div>{/if}
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
  fieldset {
    border: 0;
    padding: 0;
    margin-inline: 0;
  }
  legend {
    padding: 0;
    margin-bottom: 0.3rem;
  }
</style>
