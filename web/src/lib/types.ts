export type Perm = 'none' | 'traverse' | 'read' | 'write';

export interface Grant {
  path: string;
  permission: 'read' | 'write';
}

export interface User {
  id: number;
  username: string;
  role: 'admin' | 'user';
  quota_bytes: number;
  created_at: string;
  disabled: boolean;
  /** An admin lifted the two-factor requirement (never for admins). */
  mfa_exempt: boolean;
}

export interface Provider {
  id: string;
  name: string;
}

export interface OIDCIdentity {
  issuer: string;
  subject: string;
  created_at: string;
}

export interface Me {
  /** The password was chosen by someone else and must be replaced first. */
  must_change_password?: boolean;
  /** Set when the server has an upload bucket: files from this size go through it. */
  r2_threshold?: number;
  user: User;
  grants: Grant[] | null;
  can_write: boolean;
  totp_enabled: boolean;
  /** A second factor is required for this account. */
  totp_required: boolean;
  /** The session must enroll a second factor before anything else. */
  restricted: boolean;
  webauthn_count: number;
  /** TOTP or at least one security key is set up. */
  mfa_enabled: boolean;
  has_password: boolean;
  oidc: OIDCIdentity[] | null;
  providers: Provider[];
  share_origin: string;
  usage_bytes?: number;
}

/** A registered security key or passkey. */
export interface WebAuthnKey {
  id: string;
  name: string;
  passkey: boolean;
  created_at: string;
  last_used_at: string | null;
}

export interface Entry {
  name: string;
  path: string;
  is_dir: boolean;
  size: number;
  mtime: string;
  mime?: string;
}

export interface ArchiveEntry {
  name: string;
  size: number;
  is_dir?: boolean;
  mtime: string;
  encrypted?: boolean;
}

/** What an archive holds; `entries` is absent in the summary a FileInfo carries. */
export interface ArchiveIndex {
  format: string;
  /** Whether a password is needed, and for what. */
  encrypted: 'no' | 'entries' | 'headers' | 'unknown';
  listable: boolean;
  count: number;
  truncated?: boolean;
  entries?: ArchiveEntry[];
}

/** A file's type as the server verified it from the content. */
export interface FileInfo {
  mime: string;
  text: boolean;
  /** The name claims a different kind of file than the content is. */
  mismatch: boolean;
  ext_mime: string;
  archive?: ArchiveIndex;
}

export interface Listing {
  path: string;
  permission: Perm;
  grant_root: boolean;
  entries: Entry[];
}

export interface TrashItem {
  id: string;
  original_path: string;
  is_dir: boolean;
  size: number;
  trashed_at: string;
}

export interface Share {
  id: number;
  owner_id: number;
  path: string;
  is_dir: boolean;
  kind: 'download' | 'upload';
  expires_at: string;
  max_downloads: number;
  download_count: number;
  created_at: string;
  revoked_at: string | null;
  name: string;
  active: boolean;
  has_password: boolean;
  owner?: string;
  /** Drop links: limits (0 or empty is the server default) and what was accepted. */
  max_bytes: number;
  max_files: number;
  max_file_bytes: number;
  allowed_types: string[] | null;
  single_session: boolean;
  upload_bytes: number;
  upload_files: number;
  /** A link name chosen by the owner, used in place of the token. */
  slug: string;
  title: string;
  note: string;
  ask_name: boolean;
}

export interface ApiToken {
  id: number;
  user_id: number;
  name: string;
  /** The first characters of the token, to tell tokens apart. */
  hint: string;
  /** '' is everything its user can access. */
  paths: string[];
  caps: string[];
  allowed_ips: string[];
  created_at: string;
  expires_at: string | null;
  last_used_at: string | null;
  last_used_ip: string;
  revoked_at: string | null;
  active: boolean;
  owner?: string;
}

export interface Webhook {
  id: number;
  name: string;
  url: string;
  events: string[];
  /** Only events at or below this path; '' is everywhere. */
  path: string;
  created_at: string;
  disabled: boolean;
  last_at: string | null;
  last_status: number;
  last_error: string;
  failures: number;
}

export interface Session {
  id: string;
  user_id: number;
  created_at: string;
  last_seen_at: string;
  expires_at: string;
  ip: string;
  user_agent: string;
  auth_method: string;
  mfa: boolean;
  current: boolean;
  username?: string;
}

export interface AdminUser extends User {
  totp_enabled: boolean;
  webauthn_count: number;
  has_password: boolean;
  grants: Grant[] | null;
}

export interface Invite {
  id: number;
  role: string;
  grant_path: string | null;
  grant_perm: string | null;
  created_at: string;
  expires_at: string;
  used_at: string | null;
  used_by: number | null;
  mfa_exempt: boolean;
}

export interface AuditEntry {
  id: number;
  ts: string;
  user_id: number | null;
  username: string | null;
  ip: string;
  action: string;
  target: string;
  details: unknown;
}

export interface Stream {
  index: number;
  type: string;
  codec: string;
  width?: number;
  height?: number;
  channels?: number;
  language?: string;
  title?: string;
  default?: boolean;
}

export interface Subtitle {
  track: number;
  sidecar?: string;
  language?: string;
  title?: string;
  codec: string;
}

export interface Plan {
  mode: 'direct' | 'remux' | 'transcode' | 'hls' | 'external' | 'unsupported';
  variant?: string;
  duration: number;
  video?: Stream;
  audio: Stream[];
  audio_index: number;
  subtitles: Subtitle[];
  reason?: string;
}

export interface Job {
  path: string;
  variant: string;
  state: 'queued' | 'running' | 'done' | 'failed';
  progress: number;
  error?: string;
}

/** A locked share reports only needs_password and unlocked until it is unlocked. */
export interface ShareInfo {
  name: string;
  kind: 'download' | 'upload';
  is_dir: boolean;
  expires_at: string;
  needs_password: boolean;
  unlocked: boolean;
  size?: number;
  mime?: string;
  downloads_left?: number;
  /** Drop links: what the link still accepts; -1 (or 0 for a size) is no limit. */
  max_file_bytes?: number;
  allowed_types?: string[] | null;
  bytes_left?: number;
  files_left?: number;
  /** Set by the owner; shown in place of the plain heading. */
  title?: string;
  note?: string;
  /** Drop links: uploaders give their name, and their files go under it. */
  ask_name?: boolean;
  /** Set when the server has an upload bucket: files from this size go through it. */
  r2_threshold?: number;
}
