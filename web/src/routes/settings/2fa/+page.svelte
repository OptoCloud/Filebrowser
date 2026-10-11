<script lang="ts">
  import QRCode from 'qrcode';
  import { goto, invalidateAll } from '$app/navigation';
  import { errorMessage, post } from '#lib/api.js';
  import { session } from '#lib/session.svelte.js';

  let secret = $state('');
  let otpURL = $state('');
  let qr = $state('');
  let code = $state('');
  let err = $state('');
  let busy = $state(false);

  const me = $derived(session.me);

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
      await invalidateAll();
      goto('/settings?totp=1');
    } catch (x) {
      err = errorMessage(x);
      code = '';
    } finally {
      busy = false;
    }
  }

  async function logout() {
    try {
      await post('/api/auth/logout');
    } catch {
      /* ignore */
    }
    await invalidateAll();
    goto('/login');
  }
</script>

<svelte:head><title>Two-factor authentication</title></svelte:head>

<div class="page narrow">
  <h1>Two-factor authentication</h1>
  {#if me?.totp_enabled}
    <p>Two-factor authentication is already enabled. <a href="/settings">Back to settings</a></p>
  {:else}
    {#if me?.restricted}
      <div class="warn">
        Your account can upload, share or administer files, so an authenticator app is required
        before you can continue.
      </div>
    {/if}
    {#if err}<div class="error">{err}</div>{/if}
    {#if !secret}
      <div class="card">
        <p>
          Use an authenticator app (Aegis, 2FAS, Google Authenticator, 1Password, …) to generate
          login codes.
        </p>
        <button class="primary" onclick={setup} disabled={busy}>Set up authenticator</button>
      </div>
    {:else}
      <div class="card">
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
      </div>
    {/if}
    {#if me?.restricted}
      <p class="small"><button class="ghost small" onclick={logout}>Log out</button></p>
    {/if}
  {/if}
</div>

<style>
  .narrow {
    max-width: 560px;
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
</style>
