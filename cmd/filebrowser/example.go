package main

const exampleConfig = `# filebrowser configuration

listen = "127.0.0.1:8080"
# The authenticated UI/API origin and the public share origin. They must be
# different hosts so user content never runs alongside the session cookie.
app_origin = "https://files.example.com"
share_origin = "https://share.example.com"

# PostgreSQL connection string. Prefer FB_DATABASE_URL to keep the password
# out of this file.
database_url = "postgres://filebrowser@localhost/filebrowser?sslmode=disable"

files_root = "/srv/share"            # browsed directory; grants are relative to it
data_dir = "/var/lib/filebrowser"    # secret.key
cache_dir = "/var/cache/filebrowser" # thumbnails, remuxed media, subtitles

# Client IP header set by a trusted proxy (Cloudflare Tunnel). It is only
# believed when the connection comes from one of trusted_proxies (where
# cloudflared connects from).
trusted_ip_header = "CF-Connecting-IP"
trusted_proxies = ["127.0.0.1/32", "::1/128"]

# dev = true allows http origins and non-__Host- cookies for local testing.
dev = false

[session]
idle_timeout = "168h"
absolute_timeout = "720h"

[trash]
retention_days = 30

[shares]
default_expiry = "168h"
max_expiry = "2160h"

[uploads]
max_size = 107374182400 # bytes per file

[media]
ffmpeg = "/usr/lib/jellyfin-ffmpeg/ffmpeg"
ffprobe = "/usr/lib/jellyfin-ffmpeg/ffprobe"
# "bwrap" (Linux default) or "appcontainer" (Windows default); "none" requires
# allow_unsandboxed = true. On Windows set ffmpeg/ffprobe to the .exe paths.
sandbox = "bwrap"
# allow_unsandboxed = false
max_jobs = 2
max_jobs_per_user = 1
job_timeout = "2h"
probe_timeout = "30s"
cache_max_bytes = 214748364800
memory_limit_mb = 4096   # memory limit per ffmpeg run (prlimit / Job Object), 0 = off
cpu_seconds = 3600       # CPU-time limit per ffmpeg run (prlimit / Job Object), 0 = off
external_url_ttl = "6h"

# OIDC providers (repeat the block for more). The client secret can also be
# given as FB_OIDC_<ID>_CLIENT_SECRET.
# [[oidc]]
# id = "pocketid"
# name = "Pocket ID"
# issuer = "https://id.example.com"
# client_id = "filebrowser"
# client_secret = ""
# allowed_groups = ["filebrowser"]
# trust_mfa = true
`
