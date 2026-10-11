// Package uploads integrates tus resumable uploads.
//
// Uploads are staged in a reserved directory on the destination's filesystem
// (see rootStore) and renamed into place when complete, so they never need to
// be copied.
package uploads

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	tushandler "github.com/tus/tusd/v2/pkg/handler"
	"github.com/tus/tusd/v2/pkg/memorylocker"
	expslog "golang.org/x/exp/slog"

	"filebrowser/internal/audit"
	"filebrowser/internal/auth"
	"filebrowser/internal/files"
	"filebrowser/internal/httpx"
	"filebrowser/internal/shares"
)

const (
	AppPrefix   = "/api/uploads/"
	SharePrefix = "/_upload/"
	// ShareHeader carries the share token on share-origin upload requests.
	ShareHeader = "X-Share-Token"

	metaOwner = "fb_owner"
	metaDir   = "fb_dir"
	metaName  = "fb_name"
)

type Uploads struct {
	files   *files.Service
	shares  *shares.Service
	audit   *audit.Logger
	store   rootStore
	maxSize int64
	// failedLookup rate-limits invalid share tokens per client IP.
	failedLookup func(ip string) bool

	App   http.Handler
	Share http.Handler
}

type shareCtxKey struct{}

// failedLookup is called for each invalid share token; returning false
// answers 429 instead of 404.
func New(fsvc *files.Service, sh *shares.Service, al *audit.Logger, appOrigin, shareOrigin string, maxSize int64, failedLookup func(ip string) bool) (*Uploads, error) {
	u := &Uploads{files: fsvc, shares: sh, audit: al, maxSize: maxSize, failedLookup: failedLookup}
	u.store = rootStore{fs: fsvc.FS}
	// One process serves all uploads, so in-memory locks suffice.
	locker := memorylocker.New()

	mk := func(base string, create func(tushandler.HookEvent) (tushandler.HTTPResponse, tushandler.FileInfoChanges, error),
		finish func(tushandler.HookEvent) (tushandler.HTTPResponse, error)) (*tushandler.Handler, error) {
		composer := tushandler.NewStoreComposer()
		u.store.UseIn(composer)
		locker.UseIn(composer)
		return tushandler.NewHandler(tushandler.Config{
			BasePath:             base,
			StoreComposer:        composer,
			MaxSize:              maxSize,
			DisableDownload:      true,
			DisableConcatenation: true,
			// Same-origin only: never emit CORS headers.
			Cors:                    &tushandler.CorsConfig{Disable: true},
			PreUploadCreateCallback: create,
			PreFinishResponseCallback: func(ev tushandler.HookEvent) (tushandler.HTTPResponse, error) {
				defer fsvc.Release(ev.Upload.ID)
				return finish(ev)
			},
			PreUploadTerminateCallback: func(ev tushandler.HookEvent) (tushandler.HTTPResponse, error) {
				fsvc.Release(ev.Upload.ID)
				return tushandler.HTTPResponse{}, nil
			},
			// tusd logs every request at info level; keep only warnings.
			Logger: expslog.New(expslog.NewJSONHandler(os.Stderr, &expslog.HandlerOptions{Level: expslog.LevelWarn})).With("component", "tus"),
		})
	}
	app, err := mk(appOrigin+AppPrefix, u.appCreate, u.appFinish)
	if err != nil {
		return nil, err
	}
	share, err := mk(shareOrigin+SharePrefix, u.shareCreate, u.shareFinish)
	if err != nil {
		return nil, err
	}
	u.App = http.StripPrefix(strings.TrimSuffix(AppPrefix, "/"), u.guard(app, u.appOwner))
	u.Share = http.StripPrefix(strings.TrimSuffix(SharePrefix, "/"), u.withShare(u.guard(share, u.shareOwner)))
	return u, nil
}

func reject(status int, msg string) tushandler.Error {
	return tushandler.NewError("ERR_FB_REJECTED", msg, status)
}

// target validates upload metadata and returns the destination directory
// (relative to files_root) and file name. relativePath supports folder uploads.
func target(base string, meta tushandler.MetaData) (dir, name string, err error) {
	name = meta["filename"]
	if files.ValidName(name) != nil {
		return "", "", reject(http.StatusBadRequest, "invalid file name")
	}
	sub, err := files.Clean(meta["dir"])
	if err != nil {
		return "", "", reject(http.StatusBadRequest, "invalid directory")
	}
	dir = files.Join(base, sub)
	if rp := meta["relativePath"]; rp != "" && rp != name {
		parts := strings.Split(rp, "/")
		if len(parts) > 64 || parts[len(parts)-1] != name {
			return "", "", reject(http.StatusBadRequest, "invalid relative path")
		}
		for _, p := range parts[:len(parts)-1] {
			if files.ValidName(p) != nil {
				return "", "", reject(http.StatusBadRequest, "invalid relative path")
			}
			dir = files.Join(dir, p)
		}
	}
	if _, err := files.Clean(dir); err != nil {
		return "", "", reject(http.StatusBadRequest, "invalid directory")
	}
	return dir, name, nil
}

// reserve checks the quota and reserves the upload's bytes under a new upload
// ID, so concurrent uploads cannot together exceed the quota.
func (u *Uploads) reserve(ev tushandler.HookEvent, a *files.Access, userID, quota int64) (string, error) {
	if ev.Upload.SizeIsDeferred {
		return "", reject(http.StatusBadRequest, "upload length required")
	}
	id := auth.NewToken(16)
	if !u.files.Reserve(id, userID, a, ev.Upload.Size, quota) {
		return "", reject(http.StatusRequestEntityTooLarge, "quota exceeded")
	}
	return id, nil
}

func (u *Uploads) appCreate(ev tushandler.HookEvent) (tushandler.HTTPResponse, tushandler.FileInfoChanges, error) {
	id := httpx.IdentityFrom(ev.Context)
	if id == nil {
		return tushandler.HTTPResponse{}, tushandler.FileInfoChanges{}, reject(http.StatusUnauthorized, "unauthorized")
	}
	dir, name, err := target("", ev.Upload.MetaData)
	if err != nil {
		return tushandler.HTTPResponse{}, tushandler.FileInfoChanges{}, err
	}
	dir, err = u.files.FS.Authorize(id.Access, dir, files.PermWrite)
	if err != nil {
		return tushandler.HTTPResponse{}, tushandler.FileInfoChanges{}, reject(http.StatusForbidden, "forbidden")
	}
	uploadID, err := u.reserve(ev, id.Access, id.User.ID, id.User.QuotaBytes)
	if err != nil {
		return tushandler.HTTPResponse{}, tushandler.FileInfoChanges{}, err
	}
	return tushandler.HTTPResponse{}, u.changes(uploadID, "u:"+strconv.FormatInt(id.User.ID, 10), dir, name), nil
}

func (u *Uploads) appFinish(ev tushandler.HookEvent) (tushandler.HTTPResponse, error) {
	id := httpx.IdentityFrom(ev.Context)
	if id == nil {
		return tushandler.HTTPResponse{}, reject(http.StatusUnauthorized, "unauthorized")
	}
	final, err := u.place(ev, id.Access, "")
	if err != nil {
		return tushandler.HTTPResponse{}, err
	}
	u.files.AddUsage(id.User.ID, ev.Upload.Size)
	u.audit.Log(ev.Context, audit.FileUpload, final, map[string]any{"size": ev.Upload.Size})
	return tushandler.HTTPResponse{Header: tushandler.HTTPHeader{"X-Fb-Path": final}}, nil
}

func (u *Uploads) shareCreate(ev tushandler.HookEvent) (tushandler.HTTPResponse, tushandler.FileInfoChanges, error) {
	res, _ := ev.Context.Value(shareCtxKey{}).(*shares.Resolved)
	if res == nil || res.Share.Kind != "upload" {
		return tushandler.HTTPResponse{}, tushandler.FileInfoChanges{}, reject(http.StatusForbidden, "forbidden")
	}
	dir, name, err := target(res.Root, ev.Upload.MetaData)
	if err != nil {
		return tushandler.HTTPResponse{}, tushandler.FileInfoChanges{}, err
	}
	dir, err = u.files.FS.Authorize(res.Access, dir, files.PermWrite)
	if err != nil || !files.Within(dir, res.Root) {
		return tushandler.HTTPResponse{}, tushandler.FileInfoChanges{}, reject(http.StatusForbidden, "forbidden")
	}
	uploadID, err := u.reserve(ev, res.Access, res.Owner.ID, res.Owner.QuotaBytes)
	if err != nil {
		return tushandler.HTTPResponse{}, tushandler.FileInfoChanges{}, err
	}
	return tushandler.HTTPResponse{}, u.changes(uploadID, "s:"+strconv.FormatInt(res.Share.ID, 10), dir, name), nil
}

// changes records the upload's owner and destination, and stages its data on
// the destination's filesystem so that place can rename it into position.
func (u *Uploads) changes(uploadID, owner, dir, name string) tushandler.FileInfoChanges {
	stage, _ := u.files.FS.ReservedDir(dir, "uploads")
	return tushandler.FileInfoChanges{
		ID:       uploadID,
		MetaData: tushandler.MetaData{metaOwner: owner, metaDir: dir, metaName: name, "filename": name},
		Storage:  map[string]string{storageDir: stage},
	}
}

func (u *Uploads) shareFinish(ev tushandler.HookEvent) (tushandler.HTTPResponse, error) {
	res, _ := ev.Context.Value(shareCtxKey{}).(*shares.Resolved)
	if res == nil {
		return tushandler.HTTPResponse{}, reject(http.StatusForbidden, "forbidden")
	}
	final, err := u.place(ev, res.Access, res.Root)
	if err != nil {
		return tushandler.HTTPResponse{}, err
	}
	u.files.AddUsage(res.Owner.ID, ev.Upload.Size)
	u.audit.LogUser(ev.Context, &res.Owner.ID, audit.ShareUpload, final,
		map[string]any{"share_id": res.Share.ID, "size": ev.Upload.Size})
	// Uploaders to a drop link learn only the stored file name, not the path.
	return tushandler.HTTPResponse{Header: tushandler.HTTPHeader{"X-Fb-Name": files.Base(final)}}, nil
}

// place moves a completed upload to its destination, re-checking permissions.
// within (if not "") confines the destination for share uploads.
func (u *Uploads) place(ev tushandler.HookEvent, a *files.Access, within string) (string, error) {
	dir, name := ev.Upload.MetaData[metaDir], ev.Upload.MetaData[metaName]
	dir, err := u.files.FS.Authorize(a, dir, files.PermWrite)
	if err != nil || (within != "" && !files.Within(dir, within)) {
		return "", reject(http.StatusForbidden, "forbidden")
	}
	fsys := u.files.FS
	if err := fsys.MkdirAll(dir); err != nil {
		return "", reject(http.StatusConflict, "cannot create directory")
	}
	final, err := fsys.UniquePath(files.Join(dir, name))
	if err != nil {
		return "", err
	}
	if err := fsys.Move(binPath(ev.Upload), final); err != nil {
		return "", fmt.Errorf("move upload: %w", err)
	}
	_ = fsys.Root().Remove(infoPath(ev.Upload.ID))
	return final, nil
}

// guard ensures that only the creator of an upload can resume or cancel it.
func (u *Uploads) guard(next http.Handler, owner func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := owner(r)
		if want == "" {
			httpx.Error(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		id := strings.Trim(r.URL.Path, "/")
		if id != "" {
			if strings.ContainsAny(id, "/\\.") {
				httpx.Error(w, http.StatusNotFound, "not found")
				return
			}
			up, err := u.store.GetUpload(r.Context(), id)
			if err != nil {
				httpx.Error(w, http.StatusNotFound, "not found")
				return
			}
			info, err := up.GetInfo(r.Context())
			if err != nil || info.MetaData[metaOwner] != want {
				httpx.Error(w, http.StatusNotFound, "not found")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (u *Uploads) appOwner(r *http.Request) string {
	if id := httpx.IdentityFrom(r.Context()); id != nil {
		return "u:" + strconv.FormatInt(id.User.ID, 10)
	}
	return ""
}

func (u *Uploads) shareOwner(r *http.Request) string {
	if res, _ := r.Context().Value(shareCtxKey{}).(*shares.Resolved); res != nil {
		return "s:" + strconv.FormatInt(res.Share.ID, 10)
	}
	return ""
}

// withShare resolves the share from the token header and checks the password cookie.
func (u *Uploads) withShare(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res, err := u.shares.Resolve(r.Context(), r.Header.Get(ShareHeader))
		if errors.Is(err, shares.ErrInvalid) || (err == nil && res.Share.Kind != "upload") {
			if u.failedLookup != nil && !u.failedLookup(httpx.IP(r.Context())) {
				w.Header().Set("Retry-After", "60")
				httpx.Error(w, http.StatusTooManyRequests, "too many attempts, try again later")
				return
			}
			httpx.Error(w, http.StatusNotFound, "share not found")
			return
		} else if err != nil {
			httpx.InternalError(w, r, err)
			return
		}
		if !u.shares.Unlocked(r, res.Share) {
			httpx.Error(w, http.StatusUnauthorized, "password required")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), shareCtxKey{}, res)))
	})
}

// Cleanup removes incomplete uploads not touched for maxAge.
func (u *Uploads) Cleanup(maxAge time.Duration) {
	u.files.ReleaseOlderThan(maxAge)
	root := u.files.FS.Root()
	d, err := root.Open(files.UploadsDir)
	if err != nil {
		return
	}
	entries, err := d.ReadDir(-1)
	_ = d.Close()
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	stale := func(p string) bool {
		fi, err := root.Stat(p)
		return err != nil || fi.ModTime().Before(cutoff)
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".lock") { // left by the file locker used before
			_ = root.Remove(files.UploadsDir + "/" + name)
			continue
		}
		id, ok := strings.CutSuffix(name, ".info")
		if !ok || !stale(infoPath(id)) {
			continue
		}
		up, err := u.store.GetUpload(context.Background(), id)
		if err == nil {
			ru := up.(*rootUpload)
			if !stale(ru.binPath()) {
				continue // still being written
			}
			_ = ru.Terminate(context.Background())
			continue
		}
		// Unreadable info, or from the previous storage backend.
		_ = root.Remove(files.UploadsDir + "/" + id)
		_ = root.Remove(infoPath(id))
	}
}
