// Package uploads integrates tus resumable uploads.
//
// Uploads are staged in a reserved directory on the destination's filesystem
// (see rootStore) and renamed into place when complete, so they never need to
// be copied.
package uploads

import (
	"context"
	"errors"
	"io"
	"log/slog"
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

	// Where uploads started by older versions keep their server-side facts
	// (MetaData); new ones use Storage (see storageOwner).
	legacyOwner = "fb_owner"
	legacyDir   = "fb_dir"
	legacyName  = "fb_name"
)

type Uploads struct {
	files   *files.Service
	shares  *shares.Service
	audit   *audit.Logger
	store   rootStore
	maxSize int64
	// failedLookup rate-limits invalid share tokens per client IP, and
	// allowDrop the uploads a client starts through drop links.
	failedLookup func(ip string) bool
	allowDrop    func(ip string) bool

	App   http.Handler
	Share http.Handler

	// r2 is set when large uploads go through a bucket (see EnableR2).
	r2 *R2
}

// dropStale is how long an upload through a drop link may stand still.
const dropStale = 2 * time.Hour

type shareCtxKey struct{}

// failedLookup is called for each invalid share token, and allowDrop for each
// upload started through a drop link; returning false answers 429.
func New(fsvc *files.Service, sh *shares.Service, al *audit.Logger, appOrigin, shareOrigin string, maxSize int64,
	failedLookup, allowDrop func(ip string) bool) (*Uploads, error) {
	u := &Uploads{files: fsvc, shares: sh, audit: al, maxSize: maxSize, failedLookup: failedLookup, allowDrop: allowDrop}
	u.store = rootStore{fs: fsvc.FS}
	u.restoreReservations()
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
				u.release(ev.Context, ev.Upload)
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
		return "", "", reject(http.StatusBadRequest, `invalid file name (names cannot contain < > : " | ? * or end in a dot or space)`)
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
	if !u.files.Reserve(ev.Context, id, userID, a, ev.Upload.Size, quota) {
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
	return tushandler.HTTPResponse{}, u.changes(uploadID, userOwner(id.User.ID), id.User.ID, dir, name), nil
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
	none := tushandler.FileInfoChanges{}
	res, _ := ev.Context.Value(shareCtxKey{}).(*shares.Resolved)
	if res == nil || res.Share.Kind != "upload" {
		return tushandler.HTTPResponse{}, none, reject(http.StatusForbidden, "forbidden")
	}
	dir, name, lim, err := u.dropPlan(res, ev.Upload.MetaData, ev.Upload.Size)
	if err != nil {
		return tushandler.HTTPResponse{}, none, err
	}
	uploadID, err := u.reserve(ev, res.Access, res.Owner.ID, res.Owner.QuotaBytes)
	if err != nil {
		return tushandler.HTTPResponse{}, none, err
	}
	if err := u.reserveDrop(ev.Context, res.Share.ID, ev.Upload.Size, lim); err != nil {
		u.files.Release(uploadID)
		return tushandler.HTTPResponse{}, none, err
	}
	return tushandler.HTTPResponse{}, u.changes(uploadID, shareOwner(res.Share.ID), res.Owner.ID, dir, name), nil
}

// release gives back what an upload that will not be finished had reserved:
// its owner's quota and, through a drop link, the link's allowance.
func (u *Uploads) release(ctx context.Context, info tushandler.FileInfo) {
	u.files.Release(info.ID)
	u.releaseDrop(ctx, fact(info, storageOwner, legacyOwner), info.Size)
}

// changes records the upload's owner and destination, and stages its data on
// the destination's filesystem so that place can rename it into position.
func (u *Uploads) changes(uploadID, owner string, quotaUser int64, dir, name string) tushandler.FileInfoChanges {
	stage, _ := u.files.FS.ReservedDir(dir, "uploads")
	return tushandler.FileInfoChanges{
		ID:       uploadID,
		MetaData: tushandler.MetaData{"filename": name},
		Storage: map[string]string{storageDir: stage, storageOwner: owner,
			storageUser: strconv.FormatInt(quotaUser, 10), storageDest: dir, storageName: name},
	}
}

func (u *Uploads) shareFinish(ev tushandler.HookEvent) (tushandler.HTTPResponse, error) {
	res, _ := ev.Context.Value(shareCtxKey{}).(*shares.Resolved)
	if res == nil {
		return tushandler.HTTPResponse{}, reject(http.StatusForbidden, "forbidden")
	}
	// What the file is, by its content: the name is the uploader's word.
	if lim := u.shares.Limits(res.Share); len(lim.Types) > 0 {
		t, err := u.verify(ev.Upload)
		if err != nil || !lim.Allows(t.Mime) {
			u.discard(ev.Context, ev.Upload)
			return tushandler.HTTPResponse{}, reject(http.StatusUnsupportedMediaType, "this link does not accept this kind of file")
		}
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

// verify types a received upload by its content.
func (u *Uploads) verify(info tushandler.FileInfo) (files.Type, error) {
	f, err := u.files.FS.Root().Open(binPath(info))
	if err != nil {
		return files.Type{}, err
	}
	defer f.Close()
	return files.VerifyFile(fact(info, storageName, legacyName), f, info.Size)
}

// discard removes a received upload that is refused, with its reservations.
func (u *Uploads) discard(ctx context.Context, info tushandler.FileInfo) {
	root := u.files.FS.Root()
	_ = root.Remove(binPath(info))
	_ = root.Remove(infoPath(info.ID))
	u.release(ctx, info)
}

// place moves a completed upload to its destination, re-checking permissions.
// within (if not "") confines the destination for share uploads.
func (u *Uploads) place(ev tushandler.HookEvent, a *files.Access, within string) (string, error) {
	dir, name := fact(ev.Upload, storageDest, legacyDir), fact(ev.Upload, storageName, legacyName)
	dir, err := u.files.FS.Authorize(a, dir, files.PermWrite)
	if err != nil || (within != "" && !files.Within(dir, within)) {
		return "", reject(http.StatusForbidden, "forbidden")
	}
	final, err := u.moveIn(binPath(ev.Upload), dir, name)
	if err != nil {
		// tusd sends the error text to the client, and file errors name the
		// real path, which a drop-link uploader must not learn.
		slog.Error("place upload", "upload", ev.Upload.ID, "err", err)
		return "", reject(http.StatusConflict, "cannot store the file under this name")
	}
	_ = u.files.FS.Root().Remove(infoPath(ev.Upload.ID))
	return final, nil
}

// moveIn moves the staged file into dir (made if need be) as name, or as the
// next free variant of it: nothing is ever overwritten.
func (u *Uploads) moveIn(staged, dir, name string) (string, error) {
	fsys := u.files.FS
	if err := fsys.MkdirAll(dir); err != nil {
		return "", err
	}
	// Another upload (or a new file) can take the free name between
	// UniquePath and Move; Move never overwrites, so try the next name.
	var final string
	var err error
	for range 5 {
		if final, err = fsys.UniquePath(files.Join(dir, name)); err != nil {
			break
		}
		if err = fsys.Move(staged, final); !errors.Is(err, files.ErrExists) {
			break
		}
	}
	return final, err
}

// ErrTooLarge and ErrQuota are why Put refused a file.
var (
	ErrTooLarge = errors.New("file too large")
	ErrQuota    = errors.New("quota exceeded")
)

// Put stores body as dir/name for id in one request, for the clients that
// cannot speak tus (curl, ShareX, a camera). Like a tus upload it is staged
// on the destination's filesystem, counted against the quota and never
// overwrites; unlike one it cannot be resumed, and a request body is as
// large as the proxy in front allows (100 MB behind Cloudflare).
func (u *Uploads) Put(ctx context.Context, id *httpx.Identity, dir, name string, body io.Reader) (string, int64, error) {
	if err := files.ValidName(name); err != nil {
		return "", 0, err
	}
	// (A folder that does not exist yet resolves as far as it does, and is made.)
	dir, err := u.files.FS.Authorize(id.Access, dir, files.PermWrite)
	if err != nil {
		return "", 0, err
	}
	stage, _ := u.files.FS.ReservedDir(dir, "uploads")
	staged := stage + "/put-" + auth.NewToken(16)
	root := u.files.FS.Root()
	f, err := root.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return "", 0, err
	}
	placed := false
	defer func() {
		if !placed {
			_ = root.Remove(staged)
		}
	}()
	n, err := io.Copy(f, io.LimitReader(body, u.maxSize+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", 0, err
	}
	if n > u.maxSize {
		return "", 0, ErrTooLarge
	}
	// The size is only known now; the reservation covers the move.
	if !u.files.Reserve(ctx, staged, id.User.ID, id.Access, n, id.User.QuotaBytes) {
		return "", 0, ErrQuota
	}
	defer u.files.Release(staged)
	final, err := u.moveIn(staged, dir, name)
	if err != nil {
		return "", 0, err
	}
	placed = true
	u.files.AddUsage(id.User.ID, n)
	u.audit.Log(ctx, audit.FileUpload, final, map[string]any{"size": n})
	return final, n, nil
}

// fact reads a server-side upload fact. Uploads started before these moved
// from MetaData to Storage still carry them under the legacy MetaData key.
func fact(info tushandler.FileInfo, key, legacy string) string {
	if v, ok := info.Storage[key]; ok {
		return v
	}
	return info.MetaData[legacy]
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
			if err != nil || fact(info, storageOwner, legacyOwner) != want {
				httpx.Error(w, http.StatusNotFound, "not found")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (u *Uploads) appOwner(r *http.Request) string {
	if id := httpx.IdentityFrom(r.Context()); id != nil {
		return userOwner(id.User.ID)
	}
	return ""
}

func (u *Uploads) shareOwner(r *http.Request) string {
	if res, _ := r.Context().Value(shareCtxKey{}).(*shares.Resolved); res != nil {
		return shareOwner(res.Share.ID)
	}
	return ""
}

// withShare resolves the share from the token header and checks the password cookie.
func (u *Uploads) withShare(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res, err := u.shares.Resolve(r.Context(), r.Header.Get(ShareHeader))
		if errors.Is(err, shares.ErrInvalid) || (err == nil && res.Share.Kind != "upload") {
			if u.failedLookup != nil && !u.failedLookup(httpx.LimitKey(r.Context())) {
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
		if ok, err := u.shares.Session(r.Context(), nil, r, res.Share); err != nil {
			httpx.InternalError(w, r, err)
			return
		} else if !ok {
			httpx.ErrorCode(w, http.StatusForbidden, "in_use", "this link is in use in another browser")
			return
		}
		if r.Method == http.MethodPost && u.allowDrop != nil && !u.allowDrop(httpx.LimitKey(r.Context())) {
			w.Header().Set("Retry-After", "60")
			httpx.Error(w, http.StatusTooManyRequests, "too many uploads, try again later")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), shareCtxKey{}, res)))
	})
}

// quotaUser is the user whose quota info counts against (0: unknown, an
// upload from before that was recorded and made through a share).
func quotaUser(info tushandler.FileInfo) int64 {
	v := info.Storage[storageUser]
	if v == "" {
		v, _ = strings.CutPrefix(fact(info, storageOwner, legacyOwner), userOwnerPrefix)
	}
	id, _ := strconv.ParseInt(v, 10, 64)
	return id
}

// restoreReservations reserves the quota of the uploads in progress when the
// server last stopped; reservations are otherwise only kept in memory.
func (u *Uploads) restoreReservations() {
	for _, id := range u.pending() {
		up, err := u.store.GetUpload(context.Background(), id)
		if err != nil {
			continue
		}
		info := up.(*rootUpload).info
		if user := quotaUser(info); user != 0 {
			u.files.RestoreReservation(id, user, info.Size)
		}
	}
}

// pending lists the IDs of the uploads that have an .info file.
func (u *Uploads) pending() []string {
	d, err := u.files.FS.Root().Open(files.UploadsDir)
	if err != nil {
		return nil
	}
	names, _ := d.Readdirnames(-1)
	_ = d.Close()
	var ids []string
	for _, name := range names {
		if id, ok := strings.CutSuffix(name, ".info"); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// Cleanup removes incomplete uploads not touched for maxAge, and with them
// their quota reservations.
func (u *Uploads) Cleanup(maxAge time.Duration) {
	if u.r2 != nil {
		u.r2.Cleanup(context.Background(), maxAge)
	}
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
	// A drop link's uploads hold its owner's quota on a stranger's word, so
	// one that has stood still goes sooner.
	dropCutoff := time.Now().Add(-min(maxAge, dropStale))
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "put-") { // a one-request upload that never finished
			if stale(files.UploadsDir + "/" + name) {
				_ = root.Remove(files.UploadsDir + "/" + name)
			}
			continue
		}
		if strings.HasSuffix(name, ".lock") { // left by the file locker used before
			_ = root.Remove(files.UploadsDir + "/" + name)
			continue
		}
		id, ok := strings.CutSuffix(name, ".info")
		if !ok {
			continue
		}
		up, err := u.store.GetUpload(context.Background(), id)
		if err == nil {
			ru := up.(*rootUpload)
			fresh := !stale(ru.binPath())
			if fi, err := root.Stat(ru.binPath()); err == nil && fi.ModTime().Before(dropCutoff) &&
				strings.HasPrefix(fact(ru.info, storageOwner, legacyOwner), shareOwnerPrefix) {
				fresh = false
			}
			if fresh {
				continue // still being written
			}
			_ = ru.Terminate(context.Background())
			u.release(context.Background(), ru.info)
			continue
		}
		if !stale(infoPath(id)) {
			continue
		}
		// Unreadable info, or from the previous storage backend.
		_ = root.Remove(files.UploadsDir + "/" + id)
		_ = root.Remove(infoPath(id))
		u.files.Release(id)
	}
}
