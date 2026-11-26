package server

import (
	"net/http"

	"filebrowser/internal/httpx"
	"filebrowser/internal/uploads"
)

func (s *Server) routes() {
	// Public (allowlisted) app routes.
	s.handle("GET /api/health", s.handleHealth, public())
	s.handle("GET /api/auth/config", s.handleAuthConfig, public())
	s.handle("POST /api/auth/login", s.handleLogin, public())
	s.handle("POST /api/auth/login/totp", s.handleLoginTOTP, public())
	s.handle("POST /api/auth/login/webauthn/begin", s.handleLoginWebAuthnBegin, public())
	s.handle("POST /api/auth/login/webauthn/finish", s.handleLoginWebAuthnFinish, public())
	s.handle("POST /api/auth/passkey/begin", s.handlePasskeyBegin, public())
	s.handle("POST /api/auth/passkey/finish", s.handlePasskeyFinish, public())
	s.handle("GET /api/auth/oidc/start", s.handleOIDCStart, public())
	s.handle("GET /api/auth/oidc/callback", s.handleOIDCCallback, public())
	s.handle("GET /api/auth/invite", s.handleInviteInfo, public())
	s.handle("POST /api/auth/invite/accept", s.handleInviteAccept, public())

	// Session routes usable before TOTP enrollment.
	s.handle("GET /api/me", s.handleMe, restricted())
	s.handle("POST /api/auth/logout", s.handleLogout, restricted())
	s.handle("POST /api/auth/totp/setup", s.handleTOTPSetup, restricted())
	s.handle("POST /api/auth/totp/enable", s.handleTOTPEnable, restricted())
	s.handle("GET /api/me/webauthn", s.handleWebAuthnList, restricted())
	s.handle("POST /api/me/webauthn/register/begin", s.handleWebAuthnRegisterBegin, restricted())
	s.handle("POST /api/me/webauthn/register/finish", s.handleWebAuthnRegisterFinish, restricted())

	// Account.
	s.handle("POST /api/auth/logout-all", s.handleLogoutAll)
	s.handle("POST /api/auth/totp/disable", s.handleTOTPDisable)
	s.handle("POST /api/me/password", s.handleChangePassword)
	s.handle("GET /api/me/sessions", s.handleMySessions)
	s.handle("DELETE /api/me/sessions/{id}", s.handleDeleteMySession)
	s.handle("POST /api/me/oidc/link", s.handleOIDCLink)
	s.handle("POST /api/me/oidc/unlink", s.handleOIDCUnlink)
	s.handle("DELETE /api/me/webauthn/{id}", s.handleWebAuthnDelete)
	s.handle("GET /api/me/tokens", s.handleListTokens)
	s.handle("POST /api/me/tokens", s.handleCreateToken)
	s.handle("DELETE /api/me/tokens/{id}", s.handleRevokeToken)
	s.handle("GET /api/me/webhooks", s.handleListWebhooks)
	s.handle("POST /api/me/webhooks", s.handleCreateWebhook)
	s.handle("DELETE /api/me/webhooks/{id}", s.handleDeleteWebhook)
	s.handle("POST /api/me/webhooks/{id}/test", s.handleTestWebhook)

	// Files.
	s.handle("GET /api/files", s.handleList, token(capRead))
	s.handle("GET /api/files/download", s.handleDownload, token(capRead))
	s.handle("GET /api/files/zip", s.handleZip, token(capRead))
	s.handle("POST /api/files/zip", s.handleZip, token(capRead))
	s.handle("POST /api/files/mkdir", s.handleMkdir, token(capUpload))
	s.handle("POST /api/files/rename", s.handleRename, token(capModify))
	s.handle("POST /api/files/move", s.handleMove, token(capModify))
	s.handle("POST /api/files/delete", s.handleDelete, token(capModify))
	s.handle("POST /api/files/preview-url", s.handlePreviewURL)
	s.handle("GET /api/files/info", s.handleFileInfo, token(capRead))
	s.handle("GET /api/files/archive", s.handleArchiveIndex, token(capRead))
	s.handle("GET /api/search", s.handleSearch, token(capRead))
	s.handle("GET /api/thumb", s.handleThumb, token(capRead))
	s.handle("GET /api/trash", s.handleTrashList)
	s.handle("POST /api/trash/restore", s.handleTrashRestore)
	s.handle("POST /api/trash/purge", s.handleTrashPurge)
	s.handle(uploads.AppPrefix, s.uploads.App.ServeHTTP, noBodyLimit(), token(capUpload))

	// One-request uploads (see api_v1.go).
	s.handle("PUT /api/v1/files/{path...}", s.handlePutFile, noBodyLimit(), token(capUpload))
	s.handle("POST /api/v1/upload", s.handlePostUpload, noBodyLimit(), token(capUpload))

	// Large uploads through the bucket, when there is one (see uploads/r2.go).
	if s.r2 != nil {
		s.handle("/api/r2/", s.r2.App.ServeHTTP, token(capUpload))
		s.shareMux.Handle("/_r2/", s.r2.Share)
	}

	// Media.
	s.handle("GET /api/media/info", s.handleMediaInfo)
	s.handle("POST /api/media/prepare", s.handleMediaPrepare)
	s.handle("GET /api/media/job", s.handleMediaJob)
	s.handle("GET /api/media/stream", s.handleMediaStream)
	s.handle("GET /api/media/hls/playlist", s.handleHLSPlaylist)
	s.handle("GET /api/media/hls/init", s.handleHLSInit)
	s.handle("GET /api/media/hls/segment", s.handleHLSSegment)
	s.handle("GET /api/media/subtitle", s.handleMediaSubtitle)
	s.handle("POST /api/media/external-url", s.handleExternalURL)

	// Shares (management).
	s.handle("GET /api/shares", s.handleListShares, token(capShare))
	s.handle("POST /api/shares", s.handleCreateShare, token(capShare))
	s.handle("DELETE /api/shares/{id}", s.handleRevokeShare, token(capShare))

	// Admin.
	s.handle("GET /api/admin/users", s.handleAdminUsers, adminOnly())
	s.handle("POST /api/admin/users", s.handleAdminCreateUser, adminOnly())
	s.handle("PATCH /api/admin/users/{id}", s.handleAdminUpdateUser, adminOnly())
	s.handle("DELETE /api/admin/users/{id}", s.handleAdminDeleteUser, adminOnly())
	s.handle("PUT /api/admin/users/{id}/grants", s.handleAdminSetGrants, adminOnly())
	s.handle("POST /api/admin/users/{id}/reset-totp", s.handleAdminResetTOTP, adminOnly())
	s.handle("GET /api/admin/sessions", s.handleAdminSessions, adminOnly())
	s.handle("DELETE /api/admin/sessions/{id}", s.handleAdminDeleteSession, adminOnly())
	s.handle("GET /api/admin/invites", s.handleAdminInvites, adminOnly())
	s.handle("POST /api/admin/invites", s.handleAdminCreateInvite, adminOnly())
	s.handle("DELETE /api/admin/invites/{id}", s.handleAdminDeleteInvite, adminOnly())
	s.handle("GET /api/admin/audit", s.handleAdminAudit, adminOnly())
	s.handle("GET /api/admin/tokens", s.handleAdminTokens, adminOnly())
	s.handle("DELETE /api/admin/tokens/{id}", s.handleAdminRevokeToken, adminOnly())

	// Unknown API paths: authenticate first so nothing is revealed, then 404.
	s.handle("/api/", func(w http.ResponseWriter, r *http.Request) {
		httpx.Error(w, http.StatusNotFound, "not found")
	})
	// The SPA shell and its assets are public; all data comes from /api.
	s.appMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { s.static.serve(w, r, false) })

	// Share origin. No session cookie exists here.
	s.shareRoute("GET /s/{token}/api/info", s.handleShareInfo)
	s.shareRoute("POST /s/{token}/api/unlock", s.handleShareUnlock)
	s.shareRoute("GET /s/{token}/api/list", s.handleShareList)
	s.shareRoute("GET /s/{token}/file", s.handleShareFile)
	s.shareRoute("GET /s/{token}/zip", s.handleShareZip)
	s.shareRoute("GET /s/{token}/thumb", s.handleShareThumb)
	s.shareMux.Handle(uploads.SharePrefix, s.uploads.Share)
	s.shareMux.HandleFunc("GET /x/{token}/{name}", s.handleSignedLink)
	s.shareMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { s.static.serve(w, r, true) })
}

func (s *Server) shareRoute(pattern string, h http.HandlerFunc) {
	s.shareMux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		w.Header().Set("X-Robots-Tag", "noindex, nofollow")
		h(w, r)
	}))
}
