<script lang="ts">
  import { page } from '$app/state';
  import { del, errorMessage, get, qs } from '#lib/api.js';
  import { filesHref, formatDate, parentPath } from '#lib/format.js';
  import { LatestRequest } from '#lib/latest.js';
  import type { Share } from '#lib/types.js';
  import ConfirmDialog from '#lib/components/ConfirmDialog.svelte';

  let shares = $state<Share[]>([]);
  let loaded = $state(false);
  let err = $state('');
  let all = $state(false);
  let showInactive = $state(false);
  let revoking = $state<Share | null>(null);
  const req = new LatestRequest();

  const isAdmin = $derived(page.data.me?.user.role === 'admin');
  const visible = $derived(showInactive ? shares : shares.filter((s) => s.active));

  // Toggling "All users" quickly must not let the older response win.
  async function load() {
    const signal = req.next();
    err = '';
    try {
      const list = await get<Share[] | null>('/api/shares' + qs({ all }), { signal });
      if (signal.aborted) return;
      shares = list ?? [];
    } catch (e) {
      if (signal.aborted) return;
      err = errorMessage(e);
    }
    loaded = true;
  }

  $effect(() => {
    void all;
    load();
    return () => req.abort();
  });

  async function revoke() {
    const s = revoking;
    revoking = null;
    if (!s) return;
    try {
      await del('/api/shares/' + s.id);
    } catch (e) {
      err = errorMessage(e);
    }
    load();
  }

  function status(s: Share): string {
    if (s.revoked_at) return 'revoked';
    if (new Date(s.expires_at).getTime() < Date.now()) return 'expired';
    if (s.max_downloads > 0 && s.download_count >= s.max_downloads) return 'used up';
    return 'active';
  }
</script>

<svelte:head><title>Shares</title></svelte:head>

<div class="page">
  <div class="row head">
    <h1>Shared links</h1>
    <span class="spacer"></span>
    <label class="inline small"
      ><input type="checkbox" bind:checked={showInactive} /> Show expired/revoked</label
    >
    {#if isAdmin}<label class="inline small"
        ><input type="checkbox" bind:checked={all} /> All users</label
      >{/if}
  </div>
  <p class="muted small">
    Links are shown only once, when created. Create a share from the file browser.
  </p>
  {#if err}<div class="error">{err}</div>{/if}
  {#if !loaded}
    <p class="muted">Loading…</p>
  {:else if !visible.length}
    <p class="muted">No shares.</p>
  {:else}
    <div class="table-wrap">
      <table class="data">
        <thead>
          <tr>
            <th>Item</th>
            {#if all}<th>Owner</th>{/if}
            <th>Type</th>
            <th>Status</th>
            <th class="hide-mobile">Expires</th>
            <th class="hide-mobile">Downloads</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {#each visible as s (s.id)}
            <tr>
              <td>
                <a href={filesHref(s.is_dir ? s.path : parentPath(s.path))}>{s.name}</a>
                {#if s.has_password}<span class="badge">password</span>{/if}
                <div class="muted small">/{s.path}</div>
              </td>
              {#if all}<td>{s.owner}</td>{/if}
              <td>{s.kind === 'upload' ? 'Upload' : 'Download'}</td>
              <td
                ><span class="badge" class:ok={s.active} class:bad={!s.active}>{status(s)}</span
                ></td
              >
              <td class="hide-mobile small">{formatDate(s.expires_at)}</td>
              <td class="hide-mobile">
                {s.kind === 'download'
                  ? `${s.download_count}${s.max_downloads ? ' / ' + s.max_downloads : ''}`
                  : ''}
              </td>
              <td
                >{#if !s.revoked_at}<button class="small danger" onclick={() => (revoking = s)}
                    >Revoke</button
                  >{/if}</td
              >
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {/if}
</div>

{#if revoking}
  <ConfirmDialog
    title="Revoke link?"
    confirmLabel="Revoke"
    onconfirm={revoke}
    onclose={() => (revoking = null)}
  >
    <p>The link for "{revoking.name}" will stop working immediately.</p>
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
