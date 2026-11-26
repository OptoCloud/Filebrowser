<script lang="ts">
  import { page } from '$app/state';
  import { del, errorMessage, get, patch, post, put } from '#lib/api.js';
  import { GB, formatDate, formatSize } from '#lib/format.js';
  import type { AdminUser, Grant } from '#lib/types.js';
  import ConfirmDialog from '#lib/components/ConfirmDialog.svelte';
  import Modal from '#lib/components/Modal.svelte';
  import MfaExemptToggle from './MfaExemptToggle.svelte';

  // The accounts: who exists, what they may reach, and their sign-in state.
  let err = $state('');
  let notice = $state('');
  // Non-admins never get here (see +page.ts).
  const myId = $derived(page.data.me?.user.id);

  let users = $state<AdminUser[]>([]);
  let createOpen = $state(false);
  let nu = $state({
    username: '',
    password: '',
    role: 'user',
    quotaGB: null as number | null,
    mfaExempt: false,
  });
  let nuGrants = $state<Grant[]>([]);
  let editing = $state<AdminUser | null>(null);
  let ed = $state({
    role: 'user',
    quotaGB: null as number | null,
    disabled: false,
    mfaExempt: false,
  });
  let edGrants = $state<Grant[]>([]);
  let confirmDelete = $state<AdminUser | null>(null);
  let confirmReset = $state<AdminUser | null>(null);
  let modalErr = $state('');

  async function loadUsers() {
    try {
      users = (await get<AdminUser[] | null>('/api/admin/users')) ?? [];
    } catch (e) {
      err = errorMessage(e);
    }
  }

  async function createUser(e: Event) {
    e.preventDefault();
    modalErr = '';
    try {
      await post('/api/admin/users', {
        username: nu.username.trim(),
        password: nu.password,
        role: nu.role,
        quota_bytes: Math.round((nu.quotaGB || 0) * GB),
        grants: nu.role === 'admin' ? [] : nuGrants,
        mfa_exempt: nu.role !== 'admin' && nu.mfaExempt,
      });
      createOpen = false;
      notice = `User ${nu.username} created.`;
      nu = { username: '', password: '', role: 'user', quotaGB: null, mfaExempt: false };
      nuGrants = [];
      void loadUsers();
    } catch (x) {
      modalErr = errorMessage(x);
    }
  }

  function startEdit(u: AdminUser) {
    modalErr = '';
    editing = u;
    ed = {
      role: u.role,
      quotaGB: u.quota_bytes ? u.quota_bytes / GB : null,
      disabled: u.disabled,
      mfaExempt: u.mfa_exempt,
    };
    edGrants = (u.grants ?? []).map((g) => ({ ...g }));
  }

  async function saveEdit(e: Event) {
    e.preventDefault();
    const u = editing;
    if (!u) return;
    modalErr = '';
    try {
      await patch('/api/admin/users/' + u.id, {
        role: ed.role,
        quota_bytes: Math.round((ed.quotaGB || 0) * GB),
        disabled: ed.disabled,
        mfa_exempt: ed.role !== 'admin' && ed.mfaExempt,
      });
      await put('/api/admin/users/' + u.id + '/grants', edGrants);
      editing = null;
      notice = `Saved ${u.username}.`;
      void loadUsers();
    } catch (x) {
      modalErr = errorMessage(x);
    }
  }

  async function deleteUser() {
    const u = confirmDelete;
    confirmDelete = null;
    if (!u) return;
    try {
      await del('/api/admin/users/' + u.id);
      notice = `Deleted ${u.username}.`;
    } catch (x) {
      err = errorMessage(x);
    }
    void loadUsers();
  }

  async function resetTOTP() {
    const u = confirmReset;
    confirmReset = null;
    if (!u) return;
    try {
      await post(`/api/admin/users/${u.id}/reset-totp`);
      notice = `Two-factor reset for ${u.username}; they must set it up again at next login if required.`;
    } catch (x) {
      err = errorMessage(x);
    }
    void loadUsers();
  }

  $effect(() => {
    void loadUsers();
  });
</script>

{#snippet grantEditor(grants: Grant[])}
  <div class="grants">
    {#each grants as g, i (i)}
      <div class="row">
        <input
          class="gpath"
          bind:value={g.path}
          placeholder="path relative to the files root; empty = everything"
        />
        <select bind:value={g.permission}>
          <option value="read">read</option>
          <option value="write">write</option>
        </select>
        <button type="button" class="small" onclick={() => grants.splice(i, 1)}>Remove</button>
      </div>
    {/each}
    <button
      type="button"
      class="small"
      onclick={() => grants.push({ path: '', permission: 'read' })}
    >
      Add folder access
    </button>
  </div>
{/snippet}

{#if err}<div class="error" role="alert">{err}</div>{/if}
{#if notice}<div class="notice" role="status">{notice}</div>{/if}

<div class="row bar">
  <span class="muted small">
    {users.length} users. Admins can access everything; other users only their granted folders.
  </span>
  <span class="spacer"></span>
  <button class="primary" onclick={() => ((createOpen = true), (modalErr = ''), (nuGrants = []))}>
    New user
  </button>
</div>
<div class="table-wrap">
  <table class="data">
    <thead>
      <tr>
        <th>User</th><th>Role</th><th class="hide-mobile">Access</th><th class="hide-mobile">
          2FA
        </th><th class="hide-mobile">Quota</th><th></th>
      </tr>
    </thead>
    <tbody>
      {#each users as u (u.id)}
        <tr>
          <td>
            <strong>{u.username}</strong>
            {#if u.disabled}<span class="badge bad">disabled</span>{/if}
            <div class="muted small">since {formatDate(u.created_at)}</div>
          </td>
          <td>{u.role}</td>
          <td class="hide-mobile small">
            {#if u.role === 'admin'}<span class="muted">everything</span>
            {:else if !u.grants?.length}<span class="muted">none</span>
            {:else}
              {#each u.grants as g (g.path)}<div>
                  /{g.path} <span class="muted">({g.permission})</span>
                </div>{/each}
            {/if}
          </td>
          <td class="hide-mobile">
            {#if u.totp_enabled}<span class="badge ok">on</span>{:else}<span class="badge">
                off
              </span>{/if}
            {#if u.webauthn_count}<span class="badge ok">
                {u.webauthn_count}
                {u.webauthn_count === 1 ? 'key' : 'keys'}
              </span>{/if}
            {#if u.mfa_exempt}<span class="badge" title="Two-factor authentication is not required">
                not required
              </span>{/if}
          </td>
          <td class="hide-mobile small">{u.quota_bytes ? formatSize(u.quota_bytes) : '∞'}</td>
          <td class="actions">
            <button class="small" onclick={() => startEdit(u)}>Edit</button>
            {#if (u.totp_enabled || u.webauthn_count) && u.id !== myId}<button
                class="small"
                onclick={() => (confirmReset = u)}
              >
                Reset two-factor
              </button>{/if}
            {#if u.id !== myId}<button class="small danger" onclick={() => (confirmDelete = u)}>
                Delete
              </button>{/if}
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>
<p class="muted small">
  Passwords are reset on the server: <span class="mono">
    filebrowser user reset-password &lt;name&gt;
  </span>
</p>

{#if createOpen}
  <Modal title="New user" wide onclose={() => (createOpen = false)}>
    <form id="create-user" onsubmit={createUser}>
      <div class="two">
        <label class="field">
          Username<input bind:value={nu.username} required maxlength="64" autocomplete="off" />
        </label>
        <label class="field">
          Initial password (optional, min 10)<input
            type="password"
            bind:value={nu.password}
            minlength="10"
            autocomplete="new-password"
          />
        </label>
        <label class="field">
          Role
          <select bind:value={nu.role}>
            <option value="user">user</option><option value="admin">admin</option>
          </select>
        </label>
        <label class="field">
          Quota in GB<input
            placeholder="No limit"
            type="number"
            min="0"
            step="any"
            bind:value={nu.quotaGB}
          />
        </label>
      </div>
      {#if nu.role !== 'admin'}
        <p class="small muted">Folder access (paths relative to the files root):</p>
        {@render grantEditor(nuGrants)}
      {/if}
      {#if nu.role !== 'admin'}
        <MfaExemptToggle bind:checked={nu.mfaExempt} />
      {/if}
      <p class="small muted">
        Admins, and users with write access unless exempted, must set up two-factor authentication
        at first login.
      </p>
      {#if modalErr}<div class="error" role="alert">{modalErr}</div>{/if}
    </form>
    {#snippet footer()}
      <button onclick={() => (createOpen = false)}>Cancel</button>
      <button class="primary" type="submit" form="create-user">Create</button>
    {/snippet}
  </Modal>
{/if}

{#if editing}
  <Modal title="Edit {editing.username}" wide onclose={() => (editing = null)}>
    <form id="edit-user" onsubmit={saveEdit}>
      <div class="two">
        <label class="field">
          Role
          <select bind:value={ed.role}>
            <option value="user">user</option><option value="admin">admin</option>
          </select>
        </label>
        <label class="field">
          Quota in GB<input
            placeholder="No limit"
            type="number"
            min="0"
            step="any"
            bind:value={ed.quotaGB}
          />
        </label>
      </div>
      <label class="inline">
        <input type="checkbox" bind:checked={ed.disabled} /> Disabled (cannot log in, shares stop working)
      </label>
      {#if ed.role !== 'admin'}
        <MfaExemptToggle bind:checked={ed.mfaExempt} />
      {/if}
      <p class="small muted">
        Folder access{ed.role === 'admin' ? ' (admins can access everything regardless)' : ''}:
      </p>
      {@render grantEditor(edGrants)}
      {#if modalErr}<div class="error" role="alert">{modalErr}</div>{/if}
    </form>
    {#snippet footer()}
      <button onclick={() => (editing = null)}>Cancel</button>
      <button class="primary" type="submit" form="edit-user">Save</button>
    {/snippet}
  </Modal>
{/if}

{#if confirmDelete}
  <ConfirmDialog
    title="Delete user?"
    confirmLabel="Delete"
    onconfirm={deleteUser}
    onclose={() => (confirmDelete = null)}
  >
    <p>
      Delete <strong>{confirmDelete.username}</strong>? Their sessions, shares and trash are
      removed. Files stay on disk.
    </p>
  </ConfirmDialog>
{/if}

{#if confirmReset}
  <ConfirmDialog
    title="Reset two-factor?"
    confirmLabel="Reset"
    onconfirm={resetTOTP}
    onclose={() => (confirmReset = null)}
  >
    <p>
      {confirmReset.username} will be logged out, and their authenticator app and all security keys and
      passkeys are removed. They must set up two-factor again if their account requires it.
    </p>
  </ConfirmDialog>
{/if}
