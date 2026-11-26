// Package audit records security-relevant events.
package audit

import (
	"context"
	"log/slog"

	"filebrowser/internal/httpx"
	"filebrowser/internal/store"
)

// Event names.
const (
	LoginOK          = "auth.login"
	LoginFailed      = "auth.login_failed"
	LoginBurst       = "auth.login_burst"
	Logout           = "auth.logout"
	LogoutAll        = "auth.logout_all"
	TOTPEnabled      = "auth.totp_enabled"
	TOTPDisabled     = "auth.totp_disabled"
	TOTPFailed       = "auth.totp_failed"
	WebAuthnAdded    = "auth.webauthn_added"
	WebAuthnRemoved  = "auth.webauthn_removed"
	WebAuthnFailed   = "auth.webauthn_failed"
	PasswordChanged  = "auth.password_changed"
	OIDCLogin        = "auth.oidc_login"
	OIDCLinked       = "auth.oidc_linked"
	OIDCUnlinked     = "auth.oidc_unlinked"
	OIDCDenied       = "auth.oidc_denied"
	InviteAccepted   = "auth.invite_accepted"
	ShareCreated     = "share.created"
	ShareRevoked     = "share.revoked"
	ShareAccess      = "share.access"
	ShareUnlockFail  = "share.unlock_failed"
	ShareUpload      = "share.upload"
	TokenCreated     = "token.created"
	TokenRevoked     = "token.revoked"
	WebhookCreated   = "webhook.created"
	WebhookDeleted   = "webhook.deleted"
	FileDelete       = "file.delete"
	FileRestore      = "file.restore"
	FilePurge        = "file.purge"
	FileUpload       = "file.upload"
	AdminUserCreate  = "admin.user_create"
	AdminUserUpdate  = "admin.user_update"
	AdminUserDelete  = "admin.user_delete"
	AdminGrants      = "admin.grants"
	AdminResetTOTP   = "admin.reset_totp"
	AdminSessionKill = "admin.session_revoke"
	AdminInvite      = "admin.invite_create"
	AdminInviteDel   = "admin.invite_delete"
	CLI              = "cli"
)

type Logger struct {
	Store *store.Store
	// Notify, if set, is told of every event after it is recorded (it
	// feeds the webhooks). It must not block.
	Notify func(ctx context.Context, userID *int64, action, target string, details any)
}

// Log records an event. The acting user and IP come from ctx when present.
func (l *Logger) Log(ctx context.Context, action, target string, details any) {
	var uid *int64
	if id := httpx.IdentityFrom(ctx); id != nil {
		uid = &id.User.ID
		// What a token did is told apart from what its user did.
		if id.Token != nil {
			switch d := details.(type) {
			case nil:
				details = map[string]any{"token_id": id.Token.ID}
			case map[string]any:
				d["token_id"] = id.Token.ID
			}
		}
	}
	l.LogUser(ctx, uid, action, target, details)
}

// LogUser records an event for an explicit user (e.g. before a session exists).
func (l *Logger) LogUser(ctx context.Context, userID *int64, action, target string, details any) {
	ip := httpx.IP(ctx)
	var uid any
	if userID != nil {
		uid = *userID
	}
	slog.InfoContext(ctx, "audit", "action", action, "target", target, "ip", ip, "user_id", uid)
	if err := l.Store.AddAudit(context.WithoutCancel(ctx), userID, ip, action, target, details); err != nil {
		slog.ErrorContext(ctx, "audit write failed", "err", err, "action", action)
	}
	if l.Notify != nil {
		l.Notify(ctx, userID, action, target, details)
	}
}
