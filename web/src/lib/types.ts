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
  user: User;
  grants: Grant[] | null;
  can_write: boolean;
  totp_enabled: boolean;
  totp_required: boolean;
  restricted: boolean;
  has_password: boolean;
  oidc: OIDCIdentity[] | null;
  providers: Provider[];
  share_origin: string;
  usage_bytes?: number;
}

export interface Entry {
  name: string;
  path: string;
  is_dir: boolean;
  size: number;
  mtime: string;
  mime?: string;
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
  mode: 'direct' | 'remux' | 'transcode' | 'external' | 'unsupported';
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
}
