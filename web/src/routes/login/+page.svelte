<script lang="ts">
  import { onMount } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { ApiError, errorMessage, post } from '#lib/api.js';
  import { LatestRequest } from '#lib/latest.js';
  import { safeNext } from '#lib/nav.js';
  import {
    getCredential,
    webauthnSupported,
    type Ceremony,
    type RequestOptionsJSON,
  } from '#lib/webauthn.js';

  let { data } = $props();

  const errors: Record<string, string> = {
    oidc_failed: 'Single sign-on failed. Please try again.',
    oidc_not_linked:
      'That account is not linked to a user here. Log in with your password and link it in Settings.',
  };

  type Method = 'totp' | 'webauthn';
  interface LoginResponse {
    ok?: boolean;
    mfa_required?: boolean;
    token?: string;
    totp?: boolean;
    webauthn?: boolean;
  }

  const supported = webauthnSupported();

  let username = $state('');
  let password = $state('');
  let code = $state('');
  // The pending second-factor login (from the password step or the single sign-on redirect).
  let mfaToken = $state('');
  let methods = $state<Method[]>([]);
  let mode = $state<Method>('totp');
  let keyTried = $state(false);
  let err = $state(errors[page.url.searchParams.get('error') ?? ''] ?? '');
  let busy = $state(false);
  const providers = $derived(data.providers);
  const keyUsable = $derived(methods.includes('webauthn') && supported);
  const keyOnlyUnsupported = $derived(
    methods.includes('webauthn') && !supported && !methods.includes('totp')
  );

  // Security key ceremonies: a new one (or switching to TOTP, or leaving) cancels the previous prompt.
  const keyReq = new LatestRequest();

  onMount(() => {
    const sp = page.url.searchParams;
    const mfa = sp.get('mfa');
    const legacy = sp.get('totp');
    if (mfa) {
      const list = (sp.get('methods') ?? '')
        .split(',')
        .map((m) => m.trim())
        .filter((m): m is Method => m === 'totp' || m === 'webauthn');
      // Without a method list, offer both and let the server decide.
      startMfa(mfa, list.length ? list : ['webauthn', 'totp']);
    } else if (legacy) {
      startMfa(legacy, ['totp']);
    }
    return () => keyReq.abort();
  });

  function startMfa(token: string, list: Method[]) {
    mfaToken = token;
    methods = list;
    keyTried = false;
    code = '';
    if (list.includes('webauthn') && supported) {
      mode = 'webauthn';
      // Start the prompt once automatically; the button retries.
      useKey();
    } else {
      mode = 'totp';
      if (list.includes('webauthn') && !list.includes('totp'))
        err = 'This browser does not support security keys. Try another browser.';
    }
  }

  function resetToPassword() {
    keyReq.abort();
    busy = false;
    mfaToken = '';
    methods = [];
    code = '';
  }

  function switchMode(m: Method) {
    keyReq.abort();
    busy = false;
    err = '';
    mode = m;
    if (m === 'webauthn') useKey();
  }

  function isExpired(x: unknown): boolean {
    return (
      (x instanceof ApiError && x.code === 'login_expired') ||
      (x instanceof Error && /expired/i.test(x.message))
    );
  }

  async function done() {
    // Same-origin targets only: ?next= must not turn the login into an open redirect.
    await goto(safeNext(page.url.searchParams.get('next'), location.origin), {
      invalidateAll: true,
    });
  }

  async function login(e: Event) {
    e.preventDefault();
    busy = true;
    err = '';
    try {
      const res = await post<LoginResponse>('/api/auth/login', { username, password });
      if (res.mfa_required && res.token) {
        password = '';
        busy = false;
        const list: Method[] = [];
        if (res.webauthn) list.push('webauthn');
        if (res.totp) list.push('totp');
        startMfa(res.token, list);
        return;
      }
      await done();
    } catch (x) {
      err = errorMessage(x);
    } finally {
      if (!mfaToken) busy = false;
    }
  }

  async function verify(e: Event) {
    e.preventDefault();
    busy = true;
    err = '';
    try {
      await post('/api/auth/login/totp', { token: mfaToken, code: code.replace(/\s/g, '') });
      await done();
    } catch (x) {
      err = errorMessage(x);
      code = '';
      if (isExpired(x)) resetToPassword();
    } finally {
      busy = false;
    }
  }

  /** Runs one WebAuthn assertion against begin/finish endpoints, then logs in. */
  async function ceremony(beginURL: string, beginBody: unknown, finishURL: string) {
    const signal = keyReq.next();
    busy = true;
    err = '';
    try {
      const b = await post<Ceremony<RequestOptionsJSON>>(beginURL, beginBody, { signal });
      if (signal.aborted) return;
      const credential = await getCredential(b.options, signal);
      if (signal.aborted) return;
      await post(finishURL, { ceremony: b.ceremony, credential }, { signal });
      if (signal.aborted) return;
      await done();
    } catch (x) {
      if (signal.aborted) return;
      err = errorMessage(x);
      if (mfaToken && isExpired(x)) resetToPassword();
    } finally {
      if (!signal.aborted) busy = false;
    }
  }

  function useKey() {
    keyTried = true;
    ceremony(
      '/api/auth/login/webauthn/begin',
      { token: mfaToken },
      '/api/auth/login/webauthn/finish'
    );
  }

  function usePasskey() {
    ceremony('/api/auth/passkey/begin', {}, '/api/auth/passkey/finish');
  }
</script>

<svelte:head><title>Log in</title></svelte:head>

<div class="wrap">
  <div class="card login">
    <h1>Log in</h1>
    {#if err}<div class="error">{err}</div>{/if}
    {#if mfaToken}
      {#if mode === 'webauthn' && keyUsable}
        <p>
          {busy
            ? 'Follow the prompt from your browser to use your security key or passkey.'
            : 'Use your security key or passkey to finish logging in.'}
        </p>
        <button class="primary full" onclick={useKey} disabled={busy}>
          {busy ? 'Waiting for security key…' : keyTried ? 'Try again' : 'Use security key'}
        </button>
        {#if methods.includes('totp')}
          <button class="ghost full" onclick={() => switchMode('totp')}
            >Use an authenticator code instead</button
          >
        {/if}
      {:else if !keyOnlyUnsupported}
        <form onsubmit={verify}>
          <label class="field">
            Authentication code
            <!-- svelte-ignore a11y_autofocus -->
            <input
              bind:value={code}
              inputmode="numeric"
              autocomplete="one-time-code"
              pattern="[0-9 ]*"
              maxlength="8"
              required
              autofocus
            />
          </label>
          <button class="primary full" disabled={busy}>Verify</button>
        </form>
        {#if keyUsable}
          <button class="ghost full" onclick={() => switchMode('webauthn')}
            >Use a security key instead</button
          >
        {/if}
      {/if}
      <button type="button" class="ghost full" onclick={() => (resetToPassword(), (err = ''))}
        >Back</button
      >
    {:else}
      <form onsubmit={login}>
        <label class="field">
          Username
          <!-- svelte-ignore a11y_autofocus -->
          <input bind:value={username} autocomplete="username" required autofocus maxlength="64" />
        </label>
        <label class="field">
          Password
          <input type="password" bind:value={password} autocomplete="current-password" required />
        </label>
        <button class="primary full" disabled={busy}>Log in</button>
      </form>
      {#if supported || providers.length}
        <div class="or muted small">or</div>
      {/if}
      {#if supported}
        <button type="button" class="btn full" onclick={usePasskey} disabled={busy}
          >Sign in with a passkey</button
        >
      {/if}
      {#each providers as p (p.id)}
        <a
          class="btn full"
          href={'/api/auth/oidc/start?provider=' + encodeURIComponent(p.id)}
          data-sveltekit-reload
        >
          Continue with {p.name}
        </a>
      {/each}
    {/if}
  </div>
</div>

<style>
  .wrap {
    min-height: 100vh;
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 1rem;
  }
  .login {
    width: 100%;
    max-width: 360px;
    padding: 1.5rem;
  }
  .full {
    width: 100%;
    justify-content: center;
    margin-top: 0.4rem;
  }
  .or {
    text-align: center;
    margin: 1rem 0 0.4rem;
  }
</style>
