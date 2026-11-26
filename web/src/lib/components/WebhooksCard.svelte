<script lang="ts">
  import { ApiError, del, errorMessage, get, post } from '#lib/api.js';
  import { formatDate } from '#lib/format.js';
  import type { Webhook } from '#lib/types.js';
  import CopyField from './CopyField.svelte';
  import FolderPicker from './FolderPicker.svelte';
  import Modal from './Modal.svelte';

  let { admin = false }: { admin?: boolean } = $props();

  let hooks = $state<Webhook[]>([]);
  let err = $state('');

  async function load() {
    try {
      hooks = (await get<Webhook[] | null>('/api/me/webhooks')) ?? [];
      err = '';
    } catch (e) {
      err = errorMessage(e);
    }
  }
  $effect(() => {
    void load();
  });

  async function remove(h: Webhook) {
    try {
      await del(`/api/me/webhooks/${h.id}`);
    } catch (e) {
      err = errorMessage(e);
    }
    await load();
  }

  // The test is delivered in the background; its outcome shows a moment later.
  async function test(h: Webhook) {
    try {
      await post(`/api/me/webhooks/${h.id}/test`, {});
      setTimeout(load, 1500);
    } catch (e) {
      err = errorMessage(e);
    }
  }

  const eventNames: Record<string, string> = {
    'file.uploaded': 'I upload a file (also with a token)',
    'drop.received': 'Someone uploads through one of my drop links',
    'share.created': 'A share link is made',
    'share.accessed': 'A share link is used',
  };

  let creating = $state(false);
  let picking = $state(false);
  let name = $state('');
  let url = $state('');
  let events = $state<string[]>(['drop.received']);
  let path = $state('');
  let busy = $state(false);
  let formErr = $state('');
  let secret = $state('');

  function open() {
    creating = true;
    name = '';
    url = '';
    events = ['drop.received'];
    path = '';
    formErr = '';
    secret = '';
  }

  async function create(e: Event) {
    e.preventDefault();
    busy = true;
    formErr = '';
    try {
      const r = await post<{ secret: string }>('/api/me/webhooks', { name, url, events, path });
      secret = r.secret;
      await load();
    } catch (x) {
      formErr =
        x instanceof ApiError && x.code === 'reauth_required'
          ? 'For your security, log out and log in again before adding a webhook.'
          : errorMessage(x);
    } finally {
      busy = false;
    }
  }
</script>

<div class="card">
  <div class="row">
    <h2 class="first">Webhooks</h2>
    <span class="spacer"></span>
    <button class="small" onclick={open}>New webhook</button>
  </div>
  <p class="muted small">
    An address the server calls (a signed POST) when something happens to your files, so that other
    software can react.
  </p>
  {#if err}<div class="error" role="alert">{err}</div>{/if}
  {#if hooks.length}
    <ul class="plain">
      {#each hooks as h (h.id)}
        <li class="row">
          <span class="small">
            <strong>{h.name}</strong>
            {#if h.disabled}<span class="badge bad">disabled after failures</span>{/if}
            <br /><span class="muted">
              {h.events.join(', ')} · {h.path ? '/' + h.path : 'everywhere'}
              · {h.last_at
                ? `last call ${formatDate(h.last_at)}: ${h.last_error || 'delivered (' + h.last_status + ')'}`
                : 'never called'}
            </span>
          </span>
          <span class="spacer"></span>
          <button class="small" onclick={() => test(h)}>Test</button>
          <button class="small danger" onclick={() => remove(h)}>Delete</button>
        </li>
      {/each}
    </ul>
  {:else}
    <p class="muted small">No webhooks.</p>
  {/if}
</div>

{#if creating}
  <Modal title="New webhook" onclose={() => (creating = false)}>
    {#if secret}
      <p>
        Every call carries <code>X-Fb-Timestamp</code> and
        <code>X-Fb-Signature: sha256=…</code>, the HMAC-SHA256 of
        <code>timestamp.body</code> with this secret. Copy it now; it cannot be shown again.
      </p>
      <CopyField value={secret} />
    {:else}
      <form id="hook-form" onsubmit={create}>
        <label class="field">
          Name
          <input bind:value={name} required maxlength="60" placeholder="e.g. Paperless import" />
        </label>
        <label class="field">
          Address
          <input bind:value={url} type="url" required placeholder="https://…" />
        </label>
        <p class="muted small">
          {admin
            ? 'As an administrator you may also use http and addresses on the local network.'
            : 'Must be https and reachable from the internet.'}
        </p>
        <fieldset>
          <legend>Call it when</legend>
          {#each Object.entries(eventNames) as [value, label] (value)}
            <label class="inline">
              <input type="checkbox" bind:group={events} {value} />
              {label}
            </label>
          {/each}
        </fieldset>
        <div class="row small">
          <span>Only below: <code>{path ? '/' + path : 'everywhere'}</code></span>
          <button type="button" class="small" onclick={() => (picking = true)}>
            Choose folder
          </button>
          {#if path}<button type="button" class="small" onclick={() => (path = '')}>
              Clear
            </button>{/if}
        </div>
        {#if formErr}<div class="error" role="alert">{formErr}</div>{/if}
      </form>
    {/if}
    {#snippet footer()}
      {#if secret}
        <button class="primary" onclick={() => (creating = false)}>Done</button>
      {:else}
        <button onclick={() => (creating = false)}>Cancel</button>
        <button class="primary" type="submit" form="hook-form" disabled={busy || !events.length}>
          Create webhook
        </button>
      {/if}
    {/snippet}
  </Modal>
{/if}

{#if picking}
  <FolderPicker
    title="Folder for the webhook"
    onpick={(d) => {
      picking = false;
      path = d;
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
