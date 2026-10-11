<script lang="ts">
  import { goto, invalidate, invalidateAll } from '$app/navigation';
  import { page } from '$app/state';
  import { del, errorMessage, logout, post } from '#lib/api.js';
  import { formatDate, formatSize } from '#lib/format.js';
  import type { Session } from '#lib/types.js';
  import Modal from '#lib/components/Modal.svelte';

  let { data } = $props();

  const me = $derived(page.data.me);
  const notices: Record<string, string> = {
    oidc_already_linked: 'That single sign-on account is already linked to another user.',
  };
  let notice = $state(
    page.url.searchParams.get('linked')
      ? 'Account linked.'
      : page.url.searchParams.get('totp')
        ? 'Two-factor authentication enabled.'
        : ''
  );
  let pageErr = $state(notices[page.url.searchParams.get('error') ?? ''] ?? '');

  // Password
  let current = $state('');
  let pwCode = $state('');
  let pw1 = $state('');
  let pw2 = $state('');
  let pwMsg = $state('');
  let pwErr = $state('');

  async function changePassword(e: Event) {
    e.preventDefault();
    pwErr = pwMsg = '';
    if (pw1 !== pw2) {
      pwErr = 'Passwords do not match.';
      return;
    }
    try {
      await post('/api/me/password', { current, code: pwCode, new: pw1 });
      current = pwCode = pw1 = pw2 = '';
      pwMsg = 'Password changed. Other sessions were logged out.';
      // Reloads the account and the session list.
      await invalidateAll();
    } catch (x) {
      pwErr = errorMessage(x);
    }
  }

  // 2FA disable
  let disableOpen = $state(false);
  let dPassword = $state('');
  let dCode = $state('');
  let dErr = $state('');

  async function disableTOTP(e: Event) {
    e.preventDefault();
    dErr = '';
    try {
      await post('/api/auth/totp/disable', { password: dPassword, code: dCode.replace(/\s/g, '') });
      disableOpen = false;
      dPassword = dCode = '';
      notice = 'Two-factor authentication disabled.';
      await invalidateAll();
    } catch (x) {
      dErr = errorMessage(x);
    }
  }

  // Sessions (loaded by +page.ts)
  const sessions = $derived(data.sessions);
  let revokeErr = $state('');
  const sessErr = $derived(revokeErr || data.sessionsError);

  async function revoke(s: Session) {
    revokeErr = '';
    try {
      await del('/api/me/sessions/' + s.id);
      if (s.current) {
        await goto('/login', { invalidateAll: true });
        return;
      }
    } catch (x) {
      revokeErr = errorMessage(x);
    }
    invalidate('app:sessions');
  }

  async function unlink(issuer: string, subject: string) {
    pageErr = '';
    try {
      await post('/api/me/oidc/unlink', { issuer, subject });
      notice = 'Account unlinked.';
      await invalidateAll();
    } catch (x) {
      pageErr = errorMessage(x);
    }
  }

  function shortUA(ua: string): string {
    const m = ua.match(/(Firefox|Edg|Chrome|Safari|curl|VLC)\/[\d.]+/);
    const os = ua.match(/(Windows|Android|iPhone|iPad|Mac OS X|Linux)/);
    return [m?.[0].replace('Edg', 'Edge'), os?.[0]].filter(Boolean).join(' on ') || ua.slice(0, 60);
  }
</script>

<svelte:head><title>Settings</title></svelte:head>

{#if me}
  <div class="page narrow">
    <h1>Settings</h1>
    {#if notice}<div class="notice">{notice}</div>{/if}
    {#if pageErr}<div class="error">{pageErr}</div>{/if}

    <div class="card">
      <h2 class="first">Account</h2>
      <p>
        <strong>{me.user.username}</strong> <span class="badge">{me.user.role}</span>
        {#if me.user.quota_bytes > 0}
          <span class="muted small">
            · {formatSize(me.usage_bytes ?? 0)} of {formatSize(me.user.quota_bytes)} used</span
          >
        {/if}
      </p>
      {#if me.grants?.length}
        <p class="small muted">
          Access:
          {#each me.grants as g, i (g.path)}{i ? ', ' : ''}/{g.path} ({g.permission}){/each}
        </p>
      {/if}
    </div>

    <div class="card">
      <h2 class="first">{me.has_password ? 'Change password' : 'Set a password'}</h2>
      <form onsubmit={changePassword}>
        {#if me.has_password}
          <label class="field"
            >Current password<input
              type="password"
              bind:value={current}
              autocomplete="current-password"
              required
            /></label
          >
        {:else if me.totp_enabled}
          <label class="field"
            >Authenticator code<input
              bind:value={pwCode}
              inputmode="numeric"
              autocomplete="one-time-code"
              maxlength="6"
              required
            /></label
          >
        {:else}
          <p class="muted">
            For security, setting a first password requires a recent login. If this fails, log out
            and in again.
          </p>
        {/if}
        <label class="field"
          >New password (at least 10 characters)<input
            type="password"
            bind:value={pw1}
            autocomplete="new-password"
            required
            minlength="10"
          /></label
        >
        <label class="field"
          >Repeat new password<input
            type="password"
            bind:value={pw2}
            autocomplete="new-password"
            required
            minlength="10"
          /></label
        >
        {#if pwErr}<div class="error">{pwErr}</div>{/if}
        {#if pwMsg}<div class="notice">{pwMsg}</div>{/if}
        <button class="primary">Save password</button>
      </form>
    </div>

    <div class="card">
      <h2 class="first">Two-factor authentication</h2>
      {#if me.totp_enabled}
        <p><span class="badge ok">enabled</span></p>
        {#if me.totp_required}
          <p class="small muted">
            Required for your account. An administrator can reset it if you lose your device.
          </p>
        {:else}
          <button class="danger" onclick={() => (disableOpen = true)}>Disable</button>
        {/if}
      {:else}
        <p><span class="badge bad">not enabled</span></p>
        <a class="btn primary" href="/settings/2fa">Set up</a>
      {/if}
    </div>

    {#if me.providers.length || me.oidc?.length}
      <div class="card">
        <h2 class="first">Single sign-on</h2>
        {#if me.oidc?.length}
          <ul class="plain">
            {#each me.oidc as id (id.issuer + id.subject)}
              <li class="row">
                <span class="small"
                  ><strong>{id.issuer}</strong><br /><span class="muted mono">{id.subject}</span
                  ></span
                >
                <span class="spacer"></span>
                <button class="small danger" onclick={() => unlink(id.issuer, id.subject)}
                  >Unlink</button
                >
              </li>
            {/each}
          </ul>
        {:else}
          <p class="muted small">No linked accounts.</p>
        {/if}
        <div class="row">
          {#each me.providers as p (p.id)}
            <a
              class="btn small"
              href={'/api/auth/oidc/start?provider=' + encodeURIComponent(p.id) + '&link=1'}
              data-sveltekit-reload
            >
              Link {p.name}
            </a>
          {/each}
        </div>
      </div>
    {/if}

    <div class="card">
      <div class="row">
        <h2 class="first">Sessions</h2>
        <span class="spacer"></span>
        <button class="small danger" onclick={() => logout('/api/auth/logout-all')}
          >Log out everywhere</button
        >
      </div>
      {#if sessErr}<div class="error">{sessErr}</div>{/if}
      <ul class="plain">
        {#each sessions as s (s.id)}
          <li class="row">
            <span class="small">
              <strong>{shortUA(s.user_agent)}</strong>
              {#if s.current}<span class="badge accent">this device</span>{/if}
              <br /><span class="muted"
                >{s.ip} · {s.auth_method}{s.mfa ? ' + 2FA' : ''} · last seen {formatDate(
                  s.last_seen_at
                )}</span
              >
            </span>
            <span class="spacer"></span>
            <button class="small" onclick={() => revoke(s)}
              >{s.current ? 'Log out' : 'Revoke'}</button
            >
          </li>
        {/each}
      </ul>
    </div>
  </div>
{/if}

{#if disableOpen && me}
  <Modal title="Disable two-factor authentication" onclose={() => (disableOpen = false)}>
    <form id="disable-form" onsubmit={disableTOTP}>
      {#if me.has_password}
        <label class="field"
          >Password<input
            type="password"
            bind:value={dPassword}
            autocomplete="current-password"
            required
          /></label
        >
      {/if}
      <label class="field"
        >Current code<input
          bind:value={dCode}
          inputmode="numeric"
          autocomplete="one-time-code"
          required
        /></label
      >
      {#if dErr}<div class="error">{dErr}</div>{/if}
    </form>
    {#snippet footer()}
      <button onclick={() => (disableOpen = false)}>Cancel</button>
      <button class="danger solid" type="submit" form="disable-form">Disable</button>
    {/snippet}
  </Modal>
{/if}

<style>
  .narrow {
    max-width: 720px;
  }
  h2.first {
    margin-top: 0;
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
