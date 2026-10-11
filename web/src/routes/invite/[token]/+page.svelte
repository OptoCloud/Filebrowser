<script lang="ts">
  import { goto, invalidateAll } from '$app/navigation';
  import { page } from '$app/state';
  import { errorMessage, get, post, qs } from '#lib/api.js';
  import { formatDate } from '#lib/format.js';

  const token = $derived(page.params.token ?? '');
  let info = $state<{ role: string; expires_at: string } | null>(null);
  let loadErr = $state('');
  let username = $state('');
  let password = $state('');
  let password2 = $state('');
  let err = $state('');
  let busy = $state(false);

  $effect(() => {
    get<{ role: string; expires_at: string }>('/api/auth/invite' + qs({ token }))
      .then((i) => (info = i))
      .catch((e) => (loadErr = errorMessage(e)));
  });

  async function accept(e: Event) {
    e.preventDefault();
    err = '';
    if (password !== password2) {
      err = 'Passwords do not match.';
      return;
    }
    busy = true;
    try {
      await post('/api/auth/invite/accept', { token, username, password });
      await invalidateAll();
      goto('/files');
    } catch (x) {
      err = errorMessage(x);
    } finally {
      busy = false;
    }
  }
</script>

<svelte:head><title>Create account</title></svelte:head>

<div class="wrap">
  <div class="card box">
    <h1>Create your account</h1>
    {#if loadErr}
      <div class="error">{loadErr}</div>
    {:else if !info}
      <p class="muted">Checking invite…</p>
    {:else}
      <p class="muted small">This invite expires {formatDate(info.expires_at)}.</p>
      {#if err}<div class="error">{err}</div>{/if}
      <form onsubmit={accept}>
        <label class="field">
          Username
          <input
            bind:value={username}
            autocomplete="username"
            required
            maxlength="64"
            pattern="[A-Za-z0-9][A-Za-z0-9._\-]*"
          />
        </label>
        <label class="field">
          Password (at least 10 characters)
          <input
            type="password"
            bind:value={password}
            autocomplete="new-password"
            required
            minlength="10"
          />
        </label>
        <label class="field">
          Repeat password
          <input
            type="password"
            bind:value={password2}
            autocomplete="new-password"
            required
            minlength="10"
          />
        </label>
        <button class="primary" disabled={busy}>Create account</button>
      </form>
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
  .box {
    width: 100%;
    max-width: 400px;
    padding: 1.5rem;
  }
</style>
