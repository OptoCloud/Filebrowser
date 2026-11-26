<script lang="ts">
  import { ApiError, del, errorMessage, get, post } from '#lib/api.js';
  import { formatDate } from '#lib/format.js';
  import type { ApiToken } from '#lib/types.js';
  import CopyField from './CopyField.svelte';
  import FolderPicker from './FolderPicker.svelte';
  import Modal from './Modal.svelte';

  // admin lists everyone's tokens (to revoke); otherwise the user's own, with making new ones.
  let { admin = false }: { admin?: boolean } = $props();
  const base = $derived(admin ? '/api/admin/tokens' : '/api/me/tokens');

  let tokens = $state<ApiToken[]>([]);
  let err = $state('');
  let showOld = $state(false);
  const visible = $derived(showOld ? tokens : tokens.filter((t) => t.active));

  async function load() {
    try {
      tokens = (await get<ApiToken[] | null>(base)) ?? [];
      err = '';
    } catch (e) {
      err = errorMessage(e);
    }
  }
  $effect(() => {
    void load();
  });

  async function revoke(t: ApiToken) {
    try {
      await del(`${base}/${t.id}`);
    } catch (e) {
      err = errorMessage(e);
    }
    await load();
  }

  const capNames: Record<string, string> = {
    read: 'List and download',
    upload: 'Upload and make folders',
    modify: 'Rename, move and delete',
    share: 'Make and revoke share links',
  };
  const expiries = [
    { label: 'Never', value: 0 },
    { label: '30 days', value: 30 * 86400 },
    { label: '90 days', value: 90 * 86400 },
    { label: '1 year', value: 365 * 86400 },
  ];

  let creating = $state(false);
  let picking = $state(false);
  let name = $state('');
  let everything = $state(false);
  let paths = $state<string[]>([]);
  let caps = $state<string[]>(['upload']);
  let ips = $state('');
  let expiresIn = $state(0);
  let busy = $state(false);
  let formErr = $state('');
  let made = $state('');

  function open() {
    creating = true;
    name = '';
    everything = false;
    paths = [];
    caps = ['upload'];
    ips = '';
    expiresIn = 0;
    formErr = '';
    made = '';
  }

  async function create(e: Event) {
    e.preventDefault();
    busy = true;
    formErr = '';
    try {
      const r = await post<{ token: string }>(base, {
        name,
        paths: everything ? [''] : paths,
        caps,
        allowed_ips: ips
          .split(/[\s,]+/)
          .map((s) => s.trim())
          .filter(Boolean),
        expires_in: expiresIn,
      });
      made = r.token;
      await load();
    } catch (x) {
      formErr =
        x instanceof ApiError && x.code === 'reauth_required'
          ? 'For your security, log out and log in again before making a token.'
          : errorMessage(x);
    } finally {
      busy = false;
    }
  }
</script>

<div class="card">
  <div class="row">
    <h2 class="first">API tokens</h2>
    <span class="spacer"></span>
    {#if !admin}<button class="small" onclick={open}>New token</button>{/if}
  </div>
  {#if !admin}
    <p class="muted small">
      For scripts and other software: a token works in the folders you choose, for what you allow,
      and cannot sign in or change your account. Send it as
      <code>Authorization: Bearer &lt;token&gt;</code>.
    </p>
  {/if}
  {#if err}<div class="error" role="alert">{err}</div>{/if}
  {#if visible.length}
    <ul class="plain">
      {#each visible as t (t.id)}
        <li class="row">
          <span class="small">
            <strong>{t.name}</strong>
            <code>{t.hint}…</code>
            {#if admin && t.owner}<span class="badge">{t.owner}</span>{/if}
            {#if !t.active}<span class="badge bad">
                {t.revoked_at ? 'revoked' : 'expired'}
              </span>{/if}
            <br /><span class="muted">
              {t.paths.map((p) => (p ? '/' + p : 'everything')).join(', ')} · {t.caps.join(', ')}
              {#if t.allowed_ips.length}· from {t.allowed_ips.join(', ')}{/if}
              {#if t.expires_at}· expires {formatDate(t.expires_at)}{/if}
              · {t.last_used_at
                ? `last used ${formatDate(t.last_used_at)} from ${t.last_used_ip}`
                : 'never used'}
            </span>
          </span>
          <span class="spacer"></span>
          {#if t.active}<button class="small danger" onclick={() => revoke(t)}>Revoke</button>{/if}
        </li>
      {/each}
    </ul>
  {:else}
    <p class="muted small">No tokens.</p>
  {/if}
  {#if tokens.some((t) => !t.active)}
    <label class="inline small">
      <input type="checkbox" bind:checked={showOld} /> Show revoked and expired
    </label>
  {/if}
</div>

{#if creating}
  <Modal title="New API token" onclose={() => (creating = false)}>
    {#if made}
      <p>Copy the token now. Only a hash of it is kept, so it cannot be shown again.</p>
      <CopyField value={made} />
    {:else}
      <form id="token-form" onsubmit={create}>
        <label class="field">
          Name
          <input
            bind:value={name}
            required
            maxlength="60"
            placeholder="e.g. Scanner, backup script"
          />
        </label>
        <fieldset>
          <legend>Folders</legend>
          <label class="inline">
            <input type="checkbox" bind:checked={everything} /> Everything I can access
          </label>
          {#if !everything}
            <ul class="plain">
              {#each paths as p (p)}
                <li class="row small">
                  <code>/{p}</code><span class="spacer"></span>
                  <button
                    type="button"
                    class="small"
                    onclick={() => (paths = paths.filter((x) => x !== p))}
                  >
                    Remove
                  </button>
                </li>
              {/each}
            </ul>
            <button type="button" class="small" onclick={() => (picking = true)}>
              Add a folder
            </button>
          {/if}
        </fieldset>
        <fieldset>
          <legend>May</legend>
          {#each Object.entries(capNames) as [value, label] (value)}
            <label class="inline">
              <input type="checkbox" bind:group={caps} {value} />
              {label}
            </label>
          {/each}
        </fieldset>
        <label class="field">
          Only from these addresses (optional)
          <input bind:value={ips} placeholder="e.g. 192.168.1.0/24, 10.0.0.5" />
        </label>
        <label class="field">
          Expires
          <select bind:value={expiresIn}>
            {#each expiries as x (x.value)}<option value={x.value}>{x.label}</option>{/each}
          </select>
        </label>
        {#if formErr}<div class="error" role="alert">{formErr}</div>{/if}
      </form>
    {/if}
    {#snippet footer()}
      {#if made}
        <button class="primary" onclick={() => (creating = false)}>Done</button>
      {:else}
        <button onclick={() => (creating = false)}>Cancel</button>
        <button
          class="primary"
          type="submit"
          form="token-form"
          disabled={busy || !caps.length || (!everything && !paths.length)}
        >
          Create token
        </button>
      {/if}
    {/snippet}
  </Modal>
{/if}

{#if picking}
  <FolderPicker
    title="Folder for the token"
    onpick={(d) => {
      picking = false;
      if (d && !paths.includes(d)) paths = [...paths, d];
    }}
    onclose={() => (picking = false)}
  />
{/if}

<style>
  fieldset {
    border: 0;
    padding: 0;
    margin: 0 0 0.8rem;
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
    align-items: flex-start;
  }
  legend {
    padding: 0;
    margin-bottom: 0.3rem;
  }
  code {
    font-size: 0.85em;
  }
</style>
