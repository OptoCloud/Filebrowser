<script lang="ts">
  import { formatDate, formatSize } from '#lib/format.js';
  import { PREF, loadPref, savePref } from '#lib/prefs.js';
  import type { ShareInfo } from '#lib/types.js';
  import { Uploader, filesFromDrop, filesFromInput } from '#lib/upload.svelte.js';
  import Icon from '#lib/components/Icon.svelte';
  import UploadList from '#lib/components/UploadList.svelte';

  // The page of a drop link: files go in, nothing comes out.
  let {
    token,
    info,
    onuploaded,
  }: {
    token: string;
    info: ShareInfo;
    /** A file has arrived: what the link still accepts has changed. */
    onuploaded: () => void;
  } = $props();

  // One queue for as long as the page is open (the page is made anew for another token).
  // svelte-ignore state_referenced_locally
  const uploader = new Uploader('/_upload/', { 'X-Share-Token': token });
  uploader.onFileDone(() => onuploaded());
  $effect(() => {
    // With an upload bucket on the server, large files go through it.
    uploader.r2 =
      info.r2_threshold !== undefined ? { base: '/_r2/', threshold: info.r2_threshold } : null;
  });
  // Leaving the page stops its uploads; they could not be shown anywhere else.
  $effect(() => () => uploader.cancelAll());

  let fileInput: HTMLInputElement | undefined = $state();
  let dragging = $state(0);
  // Where the link asks for it: the uploader's name, kept for the next visit.
  let uploaderName = $state(loadPref(PREF.uploader, ''));
  const nameNeeded = $derived(!!info.ask_name && !uploaderName.trim());

  function add(files: ReturnType<typeof filesFromInput>) {
    if (!files.length || nameNeeded) return;
    if (info.ask_name) {
      uploader.extra = { uploader: uploaderName.trim() };
      savePref(PREF.uploader, uploaderName.trim());
    }
    uploader.add(files, '');
  }

  function pick() {
    if (!fileInput) return;
    add(filesFromInput(fileInput.files));
    fileInput.value = '';
  }

  async function onDrop(e: DragEvent) {
    dragging = 0;
    if (!e.dataTransfer) return;
    e.preventDefault();
    add(await filesFromDrop(e.dataTransfer));
  }

  // Closing the tab would abort running uploads.
  function onbeforeunload(e: BeforeUnloadEvent) {
    if (uploader.busy) e.preventDefault();
  }

  const typeNames: Record<string, string> = {
    image: 'images',
    video: 'video',
    audio: 'audio',
    document: 'documents',
    archive: 'archives',
  };
  // What the link still accepts, in words.
  const limits = $derived.by(() => {
    const parts: string[] = [];
    if (info.allowed_types?.length)
      parts.push(
        'Accepts ' + info.allowed_types.map((t) => typeNames[t] ?? t).join(', ') + ' only'
      );
    if (info.files_left !== undefined && info.files_left >= 0)
      parts.push(info.files_left + (info.files_left === 1 ? ' file left' : ' files left'));
    if (info.bytes_left !== undefined && info.bytes_left >= 0)
      parts.push(formatSize(info.bytes_left) + ' left');
    if (info.max_file_bytes) parts.push('up to ' + formatSize(info.max_file_bytes) + ' per file');
    return parts.join(' · ');
  });
</script>

<svelte:window {onbeforeunload} />

<div
  class="card drop"
  class:over={dragging > 0}
  role="region"
  aria-label="Upload area"
  ondragenter={(e) => (e.preventDefault(), dragging++)}
  ondragover={(e) => e.preventDefault()}
  ondragleave={() => (dragging = Math.max(0, dragging - 1))}
  ondrop={onDrop}
>
  <Icon name="upload" size={48} />
  <h1>{info.title || `Upload files to "${info.name}"`}</h1>
  {#if info.note}<p class="note">{info.note}</p>{/if}
  <p class="muted">
    Drag and drop files here, or choose them. You will not be able to see other files in this
    folder.
  </p>
  {#if info.ask_name}
    <label class="field who">
      Your name
      <input bind:value={uploaderName} maxlength="64" autocomplete="name" required />
    </label>
  {/if}
  <button class="primary" disabled={nameNeeded} onclick={() => fileInput?.click()}>
    Choose files
  </button>
  <input type="file" multiple hidden bind:this={fileInput} onchange={pick} />
  <p class="muted small">
    {limits ? limits + ' · ' : ''}Link expires {formatDate(info.expires_at)}
  </p>
</div>
{#if uploader.items.length}
  <div class="card"><UploadList {uploader} /></div>
{/if}

<style>
  .drop {
    text-align: center;
    padding: 3rem 1rem;
    border: 2px dashed var(--border);
  }
  .drop.over {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .note {
    white-space: pre-line;
    overflow-wrap: anywhere;
  }
  .who {
    max-width: 20rem;
    margin: 0 auto 0.8rem;
    text-align: left;
  }
</style>
