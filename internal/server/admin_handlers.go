package server

import (
	"bytes"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"filebrowser/internal/audit"
	"filebrowser/internal/auth"
	"filebrowser/internal/httpx"
	"filebrowser/internal/store"
)

type userView struct {
	*store.User
	TOTPEnabled bool          `json:"totp_enabled"`
	HasPassword bool          `json:"has_password"`
	Grants      []store.Grant `json:"grants"`
}

// targetUser loads the user named by the {id} path value.
func (s *Server) targetUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	uid, ok := parseID(w, r)
	if !ok {
		return nil, false
	}
	u, err := s.store.UserByID(r.Context(), uid)
	if err != nil {
		httpx.FileError(w, r, err)
		return nil, false
	}
	return u, true
}

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	out := make([]userView, 0, len(users))
	for _, u := range users {
		g, err := s.store.Grants(ctx, u.ID)
		if err != nil {
			httpx.InternalError(w, r, err)
			return
		}
		out = append(out, userView{User: u, TOTPEnabled: u.TOTPEnabled(), HasPassword: u.PasswordHash != "", Grants: g})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func validRole(r string) bool { return r == "admin" || r == "user" }

func cleanGrants(in []store.Grant) ([]store.Grant, bool) {
	out := make([]store.Grant, 0, len(in))
	for _, g := range in {
		p, ok := validGrantPath(g.Path)
		if !ok || (g.Permission != "read" && g.Permission != "write") {
			return nil, false
		}
		out = append(out, store.Grant{Path: p, Permission: g.Permission})
	}
	return out, true
}

func (s *Server) handleAdminCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username   string        `json:"username"`
		Password   string        `json:"password"`
		Role       string        `json:"role"`
		QuotaBytes int64         `json:"quota_bytes"`
		Grants     []store.Grant `json:"grants"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	if !ValidUsername(req.Username) {
		httpx.Error(w, http.StatusBadRequest, "invalid username")
		return
	}
	if req.Role == "" {
		req.Role = "user"
	}
	if !validRole(req.Role) || req.QuotaBytes < 0 {
		httpx.Error(w, http.StatusBadRequest, "invalid role or quota")
		return
	}
	grants, ok := cleanGrants(req.Grants)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "invalid grants")
		return
	}
	hash := ""
	if req.Password != "" {
		if err := auth.ValidatePassword(req.Password); err != nil {
			httpx.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		var err error
		if hash, err = auth.HashPassword(req.Password); err != nil {
			httpx.InternalError(w, r, err)
			return
		}
	}
	ctx := r.Context()
	u, err := s.store.CreateUser(ctx, req.Username, hash, req.Role, req.QuotaBytes)
	if errors.Is(err, store.ErrConflict) {
		httpx.Error(w, http.StatusConflict, "username is taken")
		return
	} else if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if err := s.store.SetGrants(ctx, u.ID, grants); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	s.audit.Log(ctx, audit.AdminUserCreate, u.Username, map[string]any{"role": u.Role, "grants": grants})
	httpx.JSON(w, http.StatusOK, userView{User: u, Grants: grants, HasPassword: hash != ""})
}

func (s *Server) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role       *string `json:"role"`
		QuotaBytes *int64  `json:"quota_bytes"`
		Disabled   *bool   `json:"disabled"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	uid := target.ID
	ctx := r.Context()
	me := httpx.IdentityFrom(ctx)
	if (req.Role != nil && !validRole(*req.Role)) || (req.QuotaBytes != nil && *req.QuotaBytes < 0) {
		httpx.Error(w, http.StatusBadRequest, "invalid role or quota")
		return
	}
	demote := req.Role != nil && *req.Role != "admin" && target.IsAdmin()
	disable := req.Disabled != nil && *req.Disabled && !target.Disabled
	if (demote || disable) && target.IsAdmin() {
		if uid == me.User.ID {
			httpx.Error(w, http.StatusBadRequest, "you cannot demote or disable yourself")
			return
		}
		if n, err := s.store.CountAdmins(ctx); err != nil || n <= 1 {
			httpx.Error(w, http.StatusBadRequest, "cannot remove the last admin")
			return
		}
	}
	if err := s.store.UpdateUser(ctx, uid, store.UserUpdate{Role: req.Role, QuotaBytes: req.QuotaBytes, Disabled: req.Disabled}); err != nil {
		httpx.FileError(w, r, err)
		return
	}
	// Privilege changes invalidate existing sessions.
	if (req.Role != nil && *req.Role != target.Role) || disable {
		if _, err := s.store.DeleteUserSessions(ctx, uid, nil); err != nil {
			httpx.InternalError(w, r, err)
			return
		}
	}
	s.files.InvalidateUsage(uid)
	s.audit.Log(ctx, audit.AdminUserUpdate, target.Username, req)
	httpx.OK(w)
}

func (s *Server) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request) {
	target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	uid := target.ID
	ctx := r.Context()
	me := httpx.IdentityFrom(ctx)
	if uid == me.User.ID {
		httpx.Error(w, http.StatusBadRequest, "you cannot delete yourself")
		return
	}
	if target.IsAdmin() {
		if n, err := s.store.CountAdmins(ctx); err != nil || n <= 1 {
			httpx.Error(w, http.StatusBadRequest, "cannot remove the last admin")
			return
		}
	}
	// Purge the user's trash first; its DB rows cascade with the user.
	if err := s.files.PurgeAll(ctx, uid); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if err := s.store.DeleteUser(ctx, uid); err != nil {
		httpx.FileError(w, r, err)
		return
	}
	s.audit.Log(ctx, audit.AdminUserDelete, target.Username, nil)
	httpx.OK(w)
}

func (s *Server) handleAdminSetGrants(w http.ResponseWriter, r *http.Request) {
	var req []store.Grant
	if !httpx.Decode(w, r, &req) {
		return
	}
	target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	uid := target.ID
	grants, ok := cleanGrants(req)
	if !ok {
		httpx.Error(w, http.StatusBadRequest, "invalid grants")
		return
	}
	ctx := r.Context()
	before, err := s.store.Grants(ctx, uid)
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	if err := s.store.SetGrants(ctx, uid, grants); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	// Gaining write access raises the TOTP requirement: rotate sessions.
	hadWrite, hasWrite := false, false
	for _, g := range before {
		hadWrite = hadWrite || g.Permission == "write"
	}
	for _, g := range grants {
		hasWrite = hasWrite || g.Permission == "write"
	}
	if hasWrite && !hadWrite && !target.TOTPEnabled() {
		if _, err := s.store.DeleteUserSessions(ctx, uid, nil); err != nil {
			httpx.InternalError(w, r, err)
			return
		}
	}
	s.files.InvalidateUsage(uid)
	s.audit.Log(ctx, audit.AdminGrants, target.Username, map[string]any{"before": before, "after": grants})
	httpx.OK(w)
}

func (s *Server) handleAdminResetTOTP(w http.ResponseWriter, r *http.Request) {
	target, ok := s.targetUser(w, r)
	if !ok {
		return
	}
	uid := target.ID
	ctx := r.Context()
	if err := s.store.DisableTOTP(ctx, uid); err != nil {
		httpx.FileError(w, r, err)
		return
	}
	if _, err := s.store.DeleteUserSessions(ctx, uid, nil); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	s.audit.Log(ctx, audit.AdminResetTOTP, target.Username, nil)
	httpx.OK(w)
}

func (s *Server) handleAdminSessions(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.AllSessions(r.Context())
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	me := httpx.IdentityFrom(r.Context())
	out := make([]sessionView, 0, len(list))
	for _, x := range list {
		sess := x.Session
		out = append(out, sessionView{ID: auth.PublicID(sess.IDHash), Session: &sess, Username: x.Username,
			Current: bytes.Equal(sess.IDHash, me.Session.IDHash)})
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (s *Server) handleAdminDeleteSession(w http.ResponseWriter, r *http.Request) {
	hash, err := auth.ParsePublicID(r.PathValue("id"))
	if err != nil {
		httpx.Error(w, http.StatusNotFound, "not found")
		return
	}
	if err := s.store.DeleteSession(r.Context(), hash); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	s.audit.Log(r.Context(), audit.AdminSessionKill, r.PathValue("id")[:12], nil)
	httpx.OK(w)
}

func (s *Server) handleAdminInvites(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.Invites(r.Context())
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, list)
}

func (s *Server) handleAdminCreateInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role      string  `json:"role"`
		GrantPath *string `json:"grant_path"`
		GrantPerm *string `json:"grant_perm"`
		ExpiresIn int64   `json:"expires_in"`
	}
	if !httpx.Decode(w, r, &req) {
		return
	}
	if req.Role == "" {
		req.Role = "user"
	}
	if !validRole(req.Role) {
		httpx.Error(w, http.StatusBadRequest, "invalid role")
		return
	}
	if (req.GrantPath == nil) != (req.GrantPerm == nil) {
		httpx.Error(w, http.StatusBadRequest, "grant_path and grant_perm go together")
		return
	}
	if req.GrantPath != nil {
		p, ok := validGrantPath(*req.GrantPath)
		if !ok || (*req.GrantPerm != "read" && *req.GrantPerm != "write") {
			httpx.Error(w, http.StatusBadRequest, "invalid grant")
			return
		}
		req.GrantPath = &p
	}
	exp := time.Duration(req.ExpiresIn) * time.Second
	if exp <= 0 || exp > 30*24*time.Hour {
		exp = 7 * 24 * time.Hour
	}
	ctx := r.Context()
	me := httpx.IdentityFrom(ctx)
	token := auth.NewToken(24)
	inv := &store.Invite{TokenHash: s.keys.HashToken(token), Role: req.Role, GrantPath: req.GrantPath,
		GrantPerm: req.GrantPerm, CreatedBy: &me.User.ID, ExpiresAt: time.Now().Add(exp).UTC()}
	if err := s.store.CreateInvite(ctx, inv); err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	s.audit.Log(ctx, audit.AdminInvite, strconv.FormatInt(inv.ID, 10), map[string]any{"role": inv.Role, "grant_path": inv.GrantPath})
	httpx.JSON(w, http.StatusOK, map[string]any{"invite": inv, "url": s.cfg.AppOrigin + "/invite/" + token})
}

func (s *Server) handleAdminDeleteInvite(w http.ResponseWriter, r *http.Request) {
	iid, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteInvite(r.Context(), iid); err != nil {
		httpx.FileError(w, r, err)
		return
	}
	s.audit.Log(r.Context(), audit.AdminInviteDel, strconv.FormatInt(iid, 10), nil)
	httpx.OK(w)
}

func (s *Server) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	uid, _ := strconv.ParseInt(q.Get("user_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	if !auditActionRe.MatchString(q.Get("action")) {
		httpx.Error(w, http.StatusBadRequest, "invalid action filter")
		return
	}
	list, err := s.store.Audit(r.Context(), store.AuditQuery{BeforeID: before, Action: q.Get("action"), UserID: uid, Limit: limit})
	if err != nil {
		httpx.InternalError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, list)
}

// auditActionRe matches action prefixes like "auth." (no LIKE wildcards).
var auditActionRe = regexp.MustCompile(`^[a-z0-9_.]{0,64}$`)
