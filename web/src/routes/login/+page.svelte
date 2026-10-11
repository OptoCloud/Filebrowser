<script lang="ts">
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { errorMessage, post } from '#lib/api.js';
  import { safeNext } from '#lib/nav.js';

  let { data } = $props();

  const errors: Record<string, string> = {
    oidc_failed: 'Single sign-on failed. Please try again.',
    oidc_not_linked:
      'That account is not linked to a user here. Log in with your password and link it in Settings.',
  };

  let username = $state('');
  let password = $state('');
  let code = $state('');
  let totpToken = $state(page.url.searchParams.get('totp') ?? '');
  let err = $state(errors[page.url.searchParams.get('error') ?? ''] ?? '');
  let busy = $state(false);
  const providers = $derived(data.providers);

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
      const res = await post<{ totp_required?: boolean; token?: string }>('/api/auth/login', {
        username,
        password,
      });
      if (res.totp_required && res.token) {
        totpToken = res.token;
        password = '';
      } else {
        await done();
      }
    } catch (x) {
      err = errorMessage(x);
    } finally {
      busy = false;
    }
  }

  async function verify(e: Event) {
    e.preventDefault();
    busy = true;
    err = '';
    try {
      await post('/api/auth/login/totp', { token: totpToken, code: code.replace(/\s/g, '') });
      await done();
    } catch (x) {
      err = errorMessage(x);
      code = '';
      if (x instanceof Error && /expired/i.test(x.message)) totpToken = '';
    } finally {
      busy = false;
    }
  }
</script>

<svelte:head><title>Log in</title></svelte:head>

<div class="wrap">
  <div class="card login">
    <h1>Log in</h1>
    {#if err}<div class="error">{err}</div>{/if}
    {#if totpToken}
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
        <button type="button" class="ghost full" onclick={() => ((totpToken = ''), (err = ''))}
          >Back</button
        >
      </form>
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
      {#if providers.length}
        <div class="or muted small">or</div>
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
