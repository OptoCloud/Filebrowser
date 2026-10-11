<script lang="ts">
  import { untrack } from 'svelte';
  import { goto } from '$app/navigation';
  import { page } from '$app/state';
  import { del, errorMessage, get, patch, post, put, qs } from '#lib/api.js';
  import { formatDate, formatSize } from '#lib/format.js';
  import type { AdminUser, AuditEntry, Grant, Invite, Session } from '#lib/types.js';
  import ConfirmDialog from '#lib/components/ConfirmDialog.svelte';
  import CopyField from '#lib/components/CopyField.svelte';
  import Modal from '#lib/components/Modal.svelte';

  type Tab = 'users' | 'sessions' | 'invites' | 'audit';
  let tab = $state<Tab>('users');
  let err = $state('');
  let notice = $state('');
  // Non-admins never get here (see +page.ts).
  const myId = $derived(page.data.me?.user.id);

  // Users
  let users = $state<AdminUser[]>([]);
  let createOpen = $state(false);
  let nu = $state({ username: '', password: '', role: 'user', quotaGB: 0 });
  let nuGrants = $state<Grant[]>([]);
  let editing = $state<AdminUser | null>(null);
  let ed = $state({ role: 'user', quotaGB: 0, disabled: false });
  let edGrants = $state<Grant[]>([]);
  let confirmDelete = $state<AdminUser | null>(null);
  let confirmReset = $state<AdminUser | null>(null);
  let modalErr = $state('');

  const GB = 1024 ** 3;

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
      });
      createOpen = false;
      notice = `User ${nu.username} created.`;
      nu = { username: '', password: '', role: 'user', quotaGB: 0 };
      nuGrants = [];
      loadUsers();
    } catch (x) {
      modalErr = errorMessage(x);
    }
  }

  function startEdit(u: AdminUser) {
    modalErr = '';
    editing = u;
    ed = { role: u.role, quotaGB: u.quota_bytes / GB, disabled: u.disabled };
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
      });
      await put('/api/admin/users/' + u.id + '/grants', edGrants);
      editing = null;
      notice = `Saved ${u.username}.`;
      loadUsers();
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
    loadUsers();
  }

  async function resetTOTP() {
    const u = confirmReset;
    confirmReset = null;
    if (!u) return;
    try {
      await post(`/api/admin/users/${u.id}/reset-totp`);
      notice = `Two-factor reset for ${u.username}; they must enroll again at next login.`;
    } catch (x) {
      err = errorMessage(x);
    }
    loadUsers();
  }

  // Sessions
  let sessions = $state<Session[]>([]);
  async function loadSessions() {
    try {
      sessions = (await get<Session[] | null>('/api/admin/sessions')) ?? [];
    } catch (e) {
      err = errorMessage(e);
    }
  }
  async function revokeSession(s: Session) {
    try {
      await del('/api/admin/sessions/' + s.id);
    } catch (x) {
      err = errorMessage(x);
    }
    if (s.current) goto('/login', { invalidateAll: true });
    else loadSessions();
  }

  // Invites
  let invites = $state<Invite[]>([]);
  let inviteOpen = $state(false);
  let inv = $state({ role: 'user', grantPath: '', grantPerm: 'read', withGrant: false, days: 7 });
  let inviteURL = $state('');
  async function loadInvites() {
    try {
      invites = (await get<Invite[] | null>('/api/admin/invites')) ?? [];
    } catch (e) {
      err = errorMessage(e);
    }
  }
  async function createInvite(e: Event) {
    e.preventDefault();
    modalErr = '';
    try {
      const r = await post<{ invite: Invite; url: string }>('/api/admin/invites', {
        role: inv.role,
        grant_path: inv.withGrant ? inv.grantPath : undefined,
        grant_perm: inv.withGrant ? inv.grantPerm : undefined,
        expires_in: inv.days * 86400,
      });
      inviteURL = r.url;
      loadInvites();
    } catch (x) {
      modalErr = errorMessage(x);
    }
  }
  async function deleteInvite(i: Invite) {
    try {
      await del('/api/admin/invites/' + i.id);
    } catch (x) {
      err = errorMessage(x);
    }
    loadInvites();
  }

  // Audit
  let audit = $state<AuditEntry[]>([]);
  let auditFilter = $state('');
  let auditMore = $state(false);
  const AUDIT_PAGE = 100;
  async function loadAudit(more = false) {
    try {
      const before = more && audit.length ? audit[audit.length - 1].id : undefined;
      const list =
        (await get<AuditEntry[] | null>(
          '/api/admin/audit' + qs({ before, action: auditFilter || undefined, limit: AUDIT_PAGE })
        )) ?? [];
      audit = more ? [...audit, ...list] : list;
      auditMore = list.length === AUDIT_PAGE;
    } catch (e) {
      err = errorMessage(e);
    }
  }

  $effect(() => {
    const t = tab;
    untrack(() => {
      err = '';
      if (t === 'users') loadUsers();
      else if (t === 'sessions') loadSessions();
      else if (t === 'invites') loadInvites();
      else loadAudit();
    });
  });

  function detailsText(d: unknown): string {
    if (d === null || d === undefined) return '';
    const s = JSON.stringify(d);
    return s.length > 200 ? s.slice(0, 200) + '…' : s;
  }
</script>

<svelte:head><title>Admin</title></svelte:head>

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
          <option value="write">read-write</option>
        </select>
        <button type="button" class="small" onclick={() => grants.splice(i, 1)}>Remove</button>
      </div>
    {/each}
    <button
      type="button"
      class="small"
      onclick={() => grants.push({ path: '', permission: 'read' })}>Add folder access</button
    >
  </div>
{/snippet}

<div class="page">
  <h1>Administration</h1>
  <div class="tabs">
    {#each ['users', 'sessions', 'invites', 'audit'] as t (t)}
      <button class:active={tab === t} onclick={() => (tab = t as Tab)}
        >{t[0].toUpperCase() + t.slice(1)}</button
      >
    {/each}
  </div>
  {#if err}<div class="error">{err}</div>{/if}
  {#if notice}<div class="notice">{notice}</div>{/if}

  {#if tab === 'users'}
    <div class="row bar">
      <span class="muted small"
        >{users.length} users. Admins can access everything; other users only their granted folders.</span
      >
      <span class="spacer"></span>
      <button
        class="primary"
        onclick={() => ((createOpen = true), (modalErr = ''), (nuGrants = []))}>New user</button
      >
    </div>
    <div class="table-wrap">
      <table class="data">
        <thead>
          <tr
            ><th>User</th><th>Role</th><th class="hide-mobile">Access</th><th class="hide-mobile"
              >2FA</th
            ><th class="hide-mobile">Quota</th><th></th></tr
          >
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
              <td class="hide-mobile"
                >{#if u.totp_enabled}<span class="badge ok">on</span>{:else}<span class="badge"
                    >off</span
                  >{/if}</td
              >
              <td class="hide-mobile small">{u.quota_bytes ? formatSize(u.quota_bytes) : '∞'}</td>
              <td class="actions">
                <button class="small" onclick={() => startEdit(u)}>Edit</button>
                {#if u.totp_enabled}<button class="small" onclick={() => (confirmReset = u)}
                    >Reset 2FA</button
                  >{/if}
                {#if u.id !== myId}<button class="small danger" onclick={() => (confirmDelete = u)}
                    >Delete</button
                  >{/if}
              </td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    <p class="muted small">
      Passwords are reset on the server: <span class="mono"
        >filebrowser user reset-password &lt;name&gt;</span
      >
    </p>
  {:else if tab === 'sessions'}
    <div class="table-wrap">
      <table class="data">
        <thead
          ><tr
            ><th>User</th><th class="hide-mobile">Client</th><th>IP</th><th class="hide-mobile"
              >Method</th
            ><th>Last seen</th><th></th></tr
          ></thead
        >
        <tbody>
          {#each sessions as s (s.id)}
            <tr>
              <td
                >{s.username}
                {#if s.current}<span class="badge accent">you</span>{/if}</td
              >
              <td class="hide-mobile small muted ua">{s.user_agent}</td>
              <td class="small">{s.ip}</td>
              <td class="hide-mobile small">{s.auth_method}{s.mfa ? ' + 2FA' : ''}</td>
              <td class="small">{formatDate(s.last_seen_at)}</td>
              <td><button class="small danger" onclick={() => revokeSession(s)}>Revoke</button></td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
  {:else if tab === 'invites'}
    <div class="row bar">
      <span class="muted small">Invite links let someone create their own account.</span>
      <span class="spacer"></span>
      <button
        class="primary"
        onclick={() => ((inviteOpen = true), (inviteURL = ''), (modalErr = ''))}>New invite</button
      >
    </div>
    <div class="table-wrap">
      <table class="data">
        <thead
          ><tr
            ><th>Created</th><th>Role</th><th>Access</th><th>Expires</th><th>Status</th><th
            ></th></tr
          ></thead
        >
        <tbody>
          {#each invites as i (i.id)}
            <tr>
              <td class="small">{formatDate(i.created_at)}</td>
              <td>{i.role}</td>
              <td class="small"
                >{i.grant_path !== null ? `/${i.grant_path} (${i.grant_perm})` : 'none'}</td
              >
              <td class="small">{formatDate(i.expires_at)}</td>
              <td>
                {#if i.used_at}<span class="badge">used</span>
                {:else if new Date(i.expires_at).getTime() < Date.now()}<span class="badge bad"
                    >expired</span
                  >
                {:else}<span class="badge ok">open</span>{/if}
              </td>
              <td><button class="small danger" onclick={() => deleteInvite(i)}>Delete</button></td>
            </tr>
          {:else}
            <tr><td colspan="6" class="muted">No invites.</td></tr>
          {/each}
        </tbody>
      </table>
    </div>
  {:else}
    <form class="row bar" onsubmit={(e) => (e.preventDefault(), loadAudit())}>
      <input
        bind:value={auditFilter}
        placeholder="Filter by action prefix (e.g. auth., share.)"
        class="filter"
      />
      <button>Filter</button>
    </form>
    <div class="table-wrap">
      <table class="data">
        <thead
          ><tr
            ><th>Time</th><th>User</th><th>Action</th><th>Target</th><th class="hide-mobile">IP</th
            ><th class="hide-mobile">Details</th></tr
          ></thead
        >
        <tbody>
          {#each audit as a (a.id)}
            <tr>
              <td class="small nowrap">{formatDate(a.ts)}</td>
              <td class="small">{a.username ?? ''}</td>
              <td class="small mono">{a.action}</td>
              <td class="small wrap target">{a.target}</td>
              <td class="small hide-mobile">{a.ip}</td>
              <td class="small mono hide-mobile wrap muted">{detailsText(a.details)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    {#if auditMore}<p><button onclick={() => loadAudit(true)}>Load older</button></p>{/if}
  {/if}
</div>

{#if createOpen}
  <Modal title="New user" wide onclose={() => (createOpen = false)}>
    <form id="create-user" onsubmit={createUser}>
      <div class="two">
        <label class="field"
          >Username<input
            bind:value={nu.username}
            required
            maxlength="64"
            autocomplete="off"
          /></label
        >
        <label class="field"
          >Initial password (optional, min 10)<input
            type="password"
            bind:value={nu.password}
            minlength="10"
            autocomplete="new-password"
          /></label
        >
        <label class="field"
          >Role
          <select bind:value={nu.role}
            ><option value="user">user</option><option value="admin">admin</option></select
          >
        </label>
        <label class="field"
          >Quota in GB (0 = none)<input
            type="number"
            min="0"
            step="any"
            bind:value={nu.quotaGB}
          /></label
        >
      </div>
      {#if nu.role !== 'admin'}
        <p class="small muted">Folder access (paths relative to the files root):</p>
        {@render grantEditor(nuGrants)}
      {/if}
      <p class="small muted">
        Users with write access or admin rights must set up two-factor authentication at first
        login.
      </p>
      {#if modalErr}<div class="error">{modalErr}</div>{/if}
    </form>
    {#snippet footer()}
      <button onclick={() => (createOpen = false)}>Cancel</button>
      <button class="primary" type="submit" form="create-user">Create</button>
    {/snippet}
  </Modal>
{/if}

{#if editing}
  <Modal title={`Edit ${editing.username}`} wide onclose={() => (editing = null)}>
    <form id="edit-user" onsubmit={saveEdit}>
      <div class="two">
        <label class="field"
          >Role
          <select bind:value={ed.role}
            ><option value="user">user</option><option value="admin">admin</option></select
          >
        </label>
        <label class="field"
          >Quota in GB (0 = none)<input
            type="number"
            min="0"
            step="any"
            bind:value={ed.quotaGB}
          /></label
        >
      </div>
      <label class="inline"
        ><input type="checkbox" bind:checked={ed.disabled} /> Disabled (cannot log in, shares stop working)</label
      >
      <p class="small muted">
        Folder access{ed.role === 'admin' ? ' (admins can access everything regardless)' : ''}:
      </p>
      {@render grantEditor(edGrants)}
      {#if modalErr}<div class="error">{modalErr}</div>{/if}
    </form>
    {#snippet footer()}
      <button onclick={() => (editing = null)}>Cancel</button>
      <button class="primary" type="submit" form="edit-user">Save</button>
    {/snippet}
  </Modal>
{/if}

{#if inviteOpen}
  <Modal title="New invite" onclose={() => (inviteOpen = false)}>
    {#if inviteURL}
      <p>Send this link to the person you are inviting:</p>
      <CopyField value={inviteURL} />
      <p class="warn small">It is shown only once.</p>
    {:else}
      <form id="invite-form" onsubmit={createInvite}>
        <label class="field"
          >Role
          <select bind:value={inv.role}
            ><option value="user">user</option><option value="admin">admin</option></select
          >
        </label>
        <label class="inline"
          ><input type="checkbox" bind:checked={inv.withGrant} /> Grant folder access</label
        >
        {#if inv.withGrant}
          <div class="row grantrow">
            <input
              class="gpath"
              bind:value={inv.grantPath}
              placeholder="path (empty = everything)"
            />
            <select bind:value={inv.grantPerm}
              ><option value="read">read</option><option value="write">read-write</option></select
            >
          </div>
        {/if}
        <label class="field"
          >Valid for (days, max 30)<input
            type="number"
            min="1"
            max="30"
            bind:value={inv.days}
          /></label
        >
        {#if modalErr}<div class="error">{modalErr}</div>{/if}
      </form>
    {/if}
    {#snippet footer()}
      {#if inviteURL}
        <button class="primary" onclick={() => (inviteOpen = false)}>Done</button>
      {:else}
        <button onclick={() => (inviteOpen = false)}>Cancel</button>
        <button class="primary" type="submit" form="invite-form">Create invite</button>
      {/if}
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
    <p>{confirmReset.username} will be logged out and must set up an authenticator again.</p>
  </ConfirmDialog>
{/if}

<style>
  .target {
    min-width: 12rem;
  }
  .bar {
    margin-bottom: 0.75rem;
  }
  .actions {
    white-space: nowrap;
  }
  .actions button {
    margin: 0.1rem;
  }
  .two {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 0 1rem;
  }
  .grants {
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
    margin-bottom: 0.8rem;
    align-items: flex-start;
  }
  .grants .row,
  .grantrow {
    width: 100%;
    flex-wrap: nowrap;
    margin: 0.4rem 0;
  }
  .gpath {
    flex: 1;
  }
  .filter {
    flex: 1;
    max-width: 360px;
  }
  .ua {
    max-width: 260px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .wrap {
    word-break: break-all;
  }
  .nowrap {
    white-space: nowrap;
  }
  @media (max-width: 640px) {
    .two {
      grid-template-columns: 1fr;
    }
  }
</style>
