<script lang="ts">
  import QRCode from 'qrcode';
  import { goto, invalidateAll } from '$app/navigation';
  import { page } from '$app/state';
  import { ApiError, del, errorMessage, logout, post } from '#lib/api.js';
  import ConfirmDialog from '#lib/components/ConfirmDialog.svelte';
  import Modal from '#lib/components/Modal.svelte';
  import { formatDate } from '#lib/format.js';
  import { LatestRequest } from '#lib/latest.js';
  import type { WebAuthnKey } from '#lib/types.js';
  import {
    createCredential,
    webauthnSupported,
    type Ceremony,
    type CreationOptionsJSON,
  } from '#lib/webauthn.js';

  let { data } = $props();

  let secret = $state('');
  let otpURL = $state('');
  let qr = $state('');
  let code = $state('');
  let err = $state('');
  let busy = $state(false);

  const me = $derived(page.data.me);
  const supported = webauthnSupported();

  async function setup() {
    err = '';
    busy = true;
    try {
      const r = await post<{ secret: string; url: string }>('/api/auth/totp/setup');
      secret = r.secret;
      otpURL = r.url;
      qr = await QRCode.toDataURL(r.url, { margin: 1, width: 220 });
    } catch (e) {
      err = errorMessage(e);
    } finally {
      busy = false;
    }
  }

  async function enable(e: Event) {
    e.preventDefault();
    err = '';
    busy = true;
    try {
      await post('/api/auth/totp/enable', { code: code.replace(/\s/g, '') });
      await goto('/settings?totp=1', { invalidateAll: true });
    } catch (x) {
      err = errorMessage(x);
      code = '';
    } finally {
      busy = false;
    }
  }

  // Security keys (loaded by +page.ts)
  const keys = $derived(data.keys);
  let keyErr = $state('');
  let keyNotice = $state('');
  const keysError = $derived(keyErr || data.keysError);

  let addOpen = $state(false);
  let keyName = $state('Security key');
  let addErr = $state('');
  let adding = $state(false);
  const addReq = new LatestRequest();
  // Leaving the page cancels a pending browser prompt.
  $effect(() => () => addReq.abort());

  function openAdd() {
    keyName = 'Security key';
    addErr = '';
    keyNotice = '';
    addOpen = true;
  }

  function closeAdd() {
    addReq.abort();
    adding = false;
    addOpen = false;
  }

  async function addKey(e: Event) {
    e.preventDefault();
    const name = keyName.trim();
    if (!name) {
      addErr = 'Enter a name for the key.';
      return;
    }
    const wasRestricted = !!me?.restricted;
    const signal = addReq.next();
    addErr = '';
    adding = true;
    try {
      const b = await post<Ceremony<CreationOptionsJSON>>(
        '/api/me/webauthn/register/begin',
        {},
        { signal }
      );
      if (signal.aborted) return;
      const credential = await createCredential(b.options, signal);
      if (signal.aborted) return;
      await post(
        '/api/me/webauthn/register/finish',
        { ceremony: b.ceremony, name, credential },
        { signal }
      );
      if (signal.aborted) return;
      addOpen = false;
      if (wasRestricted) {
        // The session is now unrestricted (the server rotated the cookie).
        await goto('/settings?key=1', { invalidateAll: true });
        return;
      }
      keyNotice = `Added "${name}".`;
      await invalidateAll();
    } catch (x) {
      if (signal.aborted) return;
      addErr =
        x instanceof ApiError && x.status === 409
          ? 'This security key is already registered to your account.'
          : errorMessage(x);
    } finally {
      if (!signal.aborted) adding = false;
    }
  }

  let confirmRemove = $state<WebAuthnKey | null>(null);

  async function removeKey() {
    const k = confirmRemove;
    confirmRemove = null;
    if (!k) return;
    keyErr = keyNotice = '';
    try {
      await del('/api/me/webauthn/' + encodeURIComponent(k.id));
      keyNotice = `Removed "${k.name}".`;
    } catch (x) {
      keyErr =
        x instanceof ApiError && x.code === 'last_second_factor'
          ? `"${k.name}" is the last second factor on your account, and your account requires two-factor authentication. Add another security key or set up an authenticator app first.`
          : errorMessage(x);
    }
    await invalidateAll();
  }
</script>

<svelte:head><title>Two-factor authentication</title></svelte:head>

<div class="page narrow">
  <h1>Two-factor authentication</h1>
  {#if me?.restricted}
    <div class="warn">
      Your account can upload, share or administer files, so a second factor is required before you
      can continue. Set up an authenticator app or add a security key (or both).
    </div>
  {/if}

  <div class="card">
    <h2 class="first">Authenticator app</h2>
    {#if me?.totp_enabled}
      <p>
        <span class="badge ok">enabled</span>
        <span class="small muted">Manage it in <a href="/settings">Settings</a>.</span>
      </p>
    {:else}
      {#if err}<div class="error">{err}</div>{/if}
      {#if !secret}
        <p>
          Use an authenticator app (Aegis, 2FAS, Google Authenticator, 1Password, …) to generate
          login codes.
        </p>
        <button class="primary" onclick={setup} disabled={busy}>Set up authenticator</button>
      {:else}
        <p>1. Scan this QR code with your authenticator app:</p>
        {#if qr}<img
            class="qr"
            src={qr}
            alt="QR code for the authenticator app"
            width="220"
            height="220"
          />{/if}
        <p class="small muted">Or enter this key manually:</p>
        <p class="mono secret">{secret}</p>
        <p class="small"><a href={otpURL}>Open in an authenticator app on this device</a></p>
        <p>2. Enter the 6-digit code it shows:</p>
        <form class="row" onsubmit={enable}>
          <input
            bind:value={code}
            inputmode="numeric"
            autocomplete="one-time-code"
            maxlength="8"
            required
            placeholder="123456"
          />
          <button class="primary" disabled={busy}>Enable</button>
        </form>
      {/if}
    {/if}
  </div>

  <div class="card">
    <h2 class="first">Security keys and passkeys</h2>
    <p class="small muted">
      Log in with a hardware key (such as a YubiKey) or a passkey stored on your phone, computer or
      password manager.
    </p>
    {#if keysError}<div class="error">{keysError}</div>{/if}
    {#if keyNotice}<div class="notice">{keyNotice}</div>{/if}
    {#if keys.length}
      <ul class="plain">
        {#each keys as k (k.id)}
          <li class="row">
            <span class="small">
              <strong>{k.name}</strong>
              {#if k.passkey}<span class="badge accent">passkey</span>{/if}
              <br /><span class="muted"
                >added {formatDate(k.created_at)} · {k.last_used_at
                  ? 'last used ' + formatDate(k.last_used_at)
                  : 'never used'}</span
              >
            </span>
            <span class="spacer"></span>
            <button class="small danger" onclick={() => (confirmRemove = k)}>Remove</button>
          </li>
        {/each}
      </ul>
    {:else if !keysError}
      <p class="muted small">No security keys yet.</p>
    {/if}
    {#if supported}
      <button class={me?.restricted ? 'primary' : ''} onclick={openAdd}>Add security key</button>
    {:else}
      <p class="small muted">This browser does not support security keys.</p>
    {/if}
  </div>

  {#if me?.restricted}
    <p class="small"><button class="ghost small" onclick={() => logout()}>Log out</button></p>
  {:else}
    <p class="small"><a href="/settings">Back to settings</a></p>
  {/if}
</div>

{#if addOpen}
  <Modal title="Add security key" onclose={closeAdd}>
    <form id="add-key-form" onsubmit={addKey}>
      <label class="field"
        >Name<input bind:value={keyName} required maxlength="64" disabled={adding} /></label
      >
      <p class="small muted">
        {adding
          ? 'Follow the prompt from your browser: insert or touch your key, or choose where to save the passkey.'
          : 'A name helps you tell your keys apart, for example "YubiKey on keychain".'}
      </p>
      {#if addErr}<div class="error">{addErr}</div>{/if}
    </form>
    {#snippet footer()}
      <button onclick={closeAdd}>Cancel</button>
      <button class="primary" type="submit" form="add-key-form" disabled={adding}
        >{adding ? 'Waiting for key…' : addErr ? 'Try again' : 'Continue'}</button
      >
    {/snippet}
  </Modal>
{/if}

{#if confirmRemove}
  <ConfirmDialog
    title="Remove security key?"
    confirmLabel="Remove"
    onconfirm={removeKey}
    onclose={() => (confirmRemove = null)}
  >
    <p>
      Remove <strong>{confirmRemove.name}</strong>? It can no longer be used to log in to this
      account.
    </p>
  </ConfirmDialog>
{/if}

<style>
  .narrow {
    max-width: 560px;
  }
  h2.first {
    margin-top: 0;
  }
  .qr {
    background: #fff;
    padding: 6px;
    border-radius: var(--radius);
  }
  .secret {
    word-break: break-all;
    user-select: all;
  }
  ul.plain {
    list-style: none;
    padding: 0;
    margin: 0 0 0.75rem;
  }
  ul.plain li {
    padding: 0.5rem 0;
    border-bottom: 1px solid var(--border);
    flex-wrap: nowrap;
  }
  ul.plain li:last-child {
    border-bottom: none;
  }
</style>
