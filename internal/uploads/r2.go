package uploads

import (
	"context"
	"crypto/md5" // #nosec G501 -- the bucket's part checksums are MD5; this checks transport, not authenticity
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	tushandler "github.com/tus/tusd/v2/pkg/handler"

	"filebrowser/internal/audit"
	"filebrowser/internal/auth"
	"filebrowser/internal/config"
	"filebrowser/internal/files"
	"filebrowser/internal/httpx"
	"filebrowser/internal/r2"
	"filebrowser/internal/shares"
	"filebrowser/internal/store"
)

// Uploads through an R2 bucket.
//
// A reverse proxy or tunnel in front of the server limits how much a request
// may carry (100 MB with Cloudflare), and every byte of a tus upload passes
// through it. With a bucket configured, a large file instead goes from the
// browser straight to the bucket, in parts, and the server pulls it from
// there afterwards: only small requests reach the server.
//
// The browser never holds the bucket's credentials. It asks the server for
// each part's address, which the server signs for that upload, that part
// number and that exact size, valid for a few minutes; the server itself
// starts and completes the upload in the bucket. So what is accepted is
// decided before any of it is sent, exactly as for a tus upload: the
// destination, the quota, and a drop link's limits. When the browser reports
// the last part, the server checks with the bucket that every part is there
// at its size, completes the object, pulls it into staging on the
// destination's filesystem (checking each part against the checksum the
// bucket holds), places it like any upload, and deletes it from the bucket.

const (
	r2PresignTTL = 15 * time.Minute
	// r2MaxParts is the most parts S3 allows in an upload.
	r2MaxParts = 10000
	// r2PullAttempts is how often a pull is tried before the upload fails.
	r2PullAttempts = 5
)

// R2 is the bucket side of Uploads; nil without a bucket.
type R2 struct {
	u      *Uploads
	client *r2.Client
	cfg    config.R2
	store  *store.Store
	wake   chan struct{}

	// App and Share are the handlers for the app origin (the caller's
	// identity in the context) and the share origin (X-Share-Token).
	App, Share http.Handler
}

// EnableR2 routes large uploads through the bucket of client.
func (u *Uploads) EnableR2(client *r2.Client, cfg config.R2) *R2 {
	r := &R2{u: u, client: client, cfg: cfg, store: u.shares.Store, wake: make(chan struct{}, 1)}
	r.App = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		id := httpx.IdentityFrom(req.Context())
		if id == nil {
			httpx.Error(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		r.serve(w, req, &r2Actor{owner: userOwner(id.User.ID), user: id.User, access: id.Access, identity: id})
	})
	r.Share = u.withShare(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		res, _ := req.Context().Value(shareCtxKey{}).(*shares.Resolved)
		if res == nil || res.Share.Kind != "upload" {
			httpx.Error(w, http.StatusForbidden, "forbidden")
			return
		}
		r.serve(w, req, &r2Actor{owner: shareOwner(res.Share.ID), user: res.Owner, access: res.Access, share: res})
	}))
	r.restoreReservations()
	u.r2 = r
	return r
}

// r2Actor is who an upload is for: a user (or a token of theirs), or the
// uploader on a drop link, whose owner's quota and access then count.
type r2Actor struct {
	owner    string
	user     *store.User
	access   *files.Access
	identity *httpx.Identity
	share    *shares.Resolved
}

func (r *R2) serve(w http.ResponseWriter, req *http.Request, a *r2Actor) {
	req.Body = http.MaxBytesReader(w, req.Body, 64<<10)
	op := req.URL.Path[strings.LastIndex(req.URL.Path, "/")+1:]
	switch {
	case req.Method == http.MethodPost && op == "begin":
		r.begin(w, req, a)
	case req.Method == http.MethodPost && op == "sign":
		r.sign(w, req, a)
	case req.Method == http.MethodPost && op == "complete":
		r.complete(w, req, a)
	case req.Method == http.MethodPost && op == "abort":
		r.abort(w, req, a)
	case req.Method == http.MethodGet && op == "status":
		r.status(w, req, a)
	default:
		httpx.Error(w, http.StatusNotFound, "not found")
	}
}

// partCount and partLen describe how a file of size is cut into parts.
func partCount(size, partSize int64) int { return int((size + partSize - 1) / partSize) }

func partLen(size, partSize int64, n int) int64 {
	if n == partCount(size, partSize) {
		return size - int64(n-1)*partSize
	}
	return partSize
}

func (r *R2) begin(w http.ResponseWriter, req *http.Request, a *r2Actor) {
	var in struct {
		Dir          string `json:"dir"`
		Name         string `json:"name"`
		RelativePath string `json:"relative_path"`
		Uploader     string `json:"uploader"`
		Size         int64  `json:"size"`
	}
	if !httpx.Decode(w, req, &in) {
		return
	}
	ctx := req.Context()
	fail := func(status int, msg string) { httpx.Error(w, status, msg) }
	if in.Size <= 0 || in.Size > r.u.maxSize {
		fail(http.StatusRequestEntityTooLarge, "file too large")
		return
	}
	meta := tushandler.MetaData{"filename": in.Name, "dir": in.Dir, "relativePath": in.RelativePath, metaUploader: in.Uploader}
	var dir, name string
	var lim shares.Limits
	var err error
	if a.share != nil {
		if r.u.allowDrop != nil && !r.u.allowDrop(httpx.LimitKey(ctx)) {
			w.Header().Set("Retry-After", "60")
			fail(http.StatusTooManyRequests, "too many uploads, try again later")
			return
		}
		dir, name, lim, err = r.u.dropPlan(a.share, meta, in.Size)
	} else if dir, name, err = target("", meta); err == nil {
		if dir, err = r.u.files.FS.Authorize(a.access, dir, files.PermWrite); err != nil {
			err = reject(http.StatusForbidden, "forbidden")
		}
	}
	if err != nil {
		fail(rejection(err))
		return
	}
	// What the bucket may come to hold at once is bounded: it is what it can cost.
	if pending, err := r.store.R2PendingBytes(ctx); err != nil {
		httpx.InternalError(w, req, err)
		return
	} else if pending+in.Size > r.cfg.MaxPendingBytes {
		w.Header().Set("Retry-After", "600")
		fail(http.StatusServiceUnavailable, "too many large uploads are in progress, try again later")
		return
	}
	id := auth.NewToken(16)
	if !r.u.files.Reserve(ctx, id, a.user.ID, a.access, in.Size, a.user.QuotaBytes) {
		fail(http.StatusRequestEntityTooLarge, "quota exceeded")
		return
	}
	undo := func() { r.u.files.Release(id) }
	if a.share != nil {
		if err := r.u.reserveDrop(ctx, a.share.Share.ID, in.Size, lim); err != nil {
			undo()
			fail(rejection(err))
			return
		}
		undo = func() {
			r.u.files.Release(id)
			r.u.releaseDrop(ctx, a.owner, in.Size)
		}
	}
	// Parts are all of one size but the last; a very large file gets larger parts.
	partSize := r.cfg.PartSize
	for partCount(in.Size, partSize) > r2MaxParts {
		partSize *= 2
	}
	up := &store.R2Upload{ID: id, Owner: a.owner, UserID: a.user.ID, DestDir: dir, Name: name, Size: in.Size,
		PartSize: partSize, ObjectKey: "uploads/" + id}
	if up.UploadID, err = r.client.CreateMultipartUpload(ctx, up.ObjectKey); err == nil {
		if err = r.store.CreateR2Upload(ctx, up); err != nil {
			_ = r.client.AbortMultipartUpload(context.WithoutCancel(ctx), up.ObjectKey, up.UploadID)
		}
	}
	if err != nil {
		undo()
		slog.Error("r2 begin", "err", err)
		fail(http.StatusBadGateway, "the upload could not be started")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"id": id, "part_size": partSize, "parts": partCount(in.Size, partSize)})
}

// own loads the upload id if it is the actor's and in state.
func (r *R2) own(w http.ResponseWriter, req *http.Request, a *r2Actor, id, state string) *store.R2Upload {
	up, err := r.store.R2Upload(req.Context(), id)
	if err != nil || up.Owner != a.owner {
		httpx.Error(w, http.StatusNotFound, "not found")
		return nil
	}
	if state != "" && up.State != state {
		httpx.ErrorCode(w, http.StatusConflict, "upload_"+up.State, "this upload is "+up.State)
		return nil
	}
	return up
}

func (r *R2) sign(w http.ResponseWriter, req *http.Request, a *r2Actor) {
	var in struct {
		ID    string `json:"id"`
		Parts []int  `json:"parts"`
	}
	if !httpx.Decode(w, req, &in) {
		return
	}
	up := r.own(w, req, a, in.ID, store.R2Uploading)
	if up == nil {
		return
	}
	total := partCount(up.Size, up.PartSize)
	if len(in.Parts) == 0 || len(in.Parts) > 32 {
		httpx.Error(w, http.StatusBadRequest, "ask for 1 to 32 parts")
		return
	}
	urls := map[string]string{}
	for _, n := range in.Parts {
		if n < 1 || n > total {
			httpx.Error(w, http.StatusBadRequest, "no such part")
			return
		}
		urls[strconv.Itoa(n)] = r.client.PresignPart(up.ObjectKey, up.UploadID, n, partLen(up.Size, up.PartSize, n), r2PresignTTL)
	}
	_ = r.store.TouchR2Upload(req.Context(), up.ID)
	httpx.JSON(w, http.StatusOK, map[string]any{"urls": urls})
}

// have lists the parts the bucket holds at the size they must have.
func (r *R2) have(ctx context.Context, up *store.R2Upload) (good []r2.Part, missing []int, err error) {
	parts, err := r.client.ListParts(ctx, up.ObjectKey, up.UploadID)
	if err != nil {
		return nil, nil, err
	}
	byNumber := map[int]r2.Part{}
	for _, p := range parts {
		byNumber[p.Number] = p
	}
	for n := 1; n <= partCount(up.Size, up.PartSize); n++ {
		if p, ok := byNumber[n]; ok && p.Size == partLen(up.Size, up.PartSize, n) {
			good = append(good, p)
		} else {
			missing = append(missing, n)
		}
	}
	return good, missing, nil
}

func (r *R2) complete(w http.ResponseWriter, req *http.Request, a *r2Actor) {
	var in struct {
		ID string `json:"id"`
	}
	if !httpx.Decode(w, req, &in) {
		return
	}
	up := r.own(w, req, a, in.ID, store.R2Uploading)
	if up == nil {
		return
	}
	ctx := req.Context()
	parts, missing, err := r.have(ctx, up)
	if err != nil {
		slog.Error("r2 list parts", "upload", up.ID, "err", err)
		httpx.Error(w, http.StatusBadGateway, "the upload could not be checked")
		return
	}
	if len(missing) > 0 {
		httpx.JSON(w, http.StatusConflict, map[string]any{"error": "parts are missing", "code": "parts_missing", "missing": missing})
		return
	}
	raw, _ := json.Marshal(parts)
	if err := r.store.SetR2Parts(ctx, up.ID, raw); err != nil {
		httpx.InternalError(w, req, err)
		return
	}
	// From here the upload is the server's to finish, whatever the browser does.
	ctx = context.WithoutCancel(ctx)
	if err := r.client.CompleteMultipartUpload(ctx, up.ObjectKey, up.UploadID, parts); err != nil {
		slog.Error("r2 complete", "upload", up.ID, "err", err)
		httpx.Error(w, http.StatusBadGateway, "the upload could not be completed")
		return
	}
	if n, err := r.client.Head(ctx, up.ObjectKey); err != nil || n != up.Size {
		slog.Error("r2 completed object", "upload", up.ID, "size", n, "want", up.Size, "err", err)
		r.fail(ctx, up, "the completed upload does not have the announced size")
		httpx.Error(w, http.StatusConflict, "the upload does not have the announced size")
		return
	}
	if ok, err := r.store.MoveR2Upload(ctx, up.ID, store.R2Uploading, store.R2Uploaded); err != nil || !ok {
		httpx.Error(w, http.StatusConflict, "this upload was already completed")
		return
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"state": store.R2Uploaded})
}

func (r *R2) status(w http.ResponseWriter, req *http.Request, a *r2Actor) {
	up := r.own(w, req, a, req.URL.Query().Get("id"), "")
	if up == nil {
		return
	}
	out := map[string]any{"state": up.State, "size": up.Size, "part_size": up.PartSize, "parts": partCount(up.Size, up.PartSize)}
	switch up.State {
	case store.R2Uploading:
		// What is already there, for an upload that is taken up again.
		good, _, err := r.have(req.Context(), up)
		if err != nil {
			httpx.Error(w, http.StatusBadGateway, "the upload could not be checked")
			return
		}
		have := make([]int, 0, len(good))
		for _, p := range good {
			have = append(have, p.Number)
		}
		out["have"] = have
	case store.R2Done:
		// A drop link's uploader learns the stored name, not the path.
		out["name"] = files.Base(up.FinalPath)
		if a.share == nil {
			out["path"] = up.FinalPath
		}
	case store.R2Failed, store.R2Expired:
		out["error"] = up.Error
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (r *R2) abort(w http.ResponseWriter, req *http.Request, a *r2Actor) {
	var in struct {
		ID string `json:"id"`
	}
	if !httpx.Decode(w, req, &in) {
		return
	}
	up := r.own(w, req, a, in.ID, store.R2Uploading)
	if up == nil {
		return
	}
	r.expire(context.WithoutCancel(req.Context()), up, "cancelled")
	httpx.OK(w)
}

// release gives back the quota and, for a drop link, the allowance of up.
func (r *R2) release(ctx context.Context, up *store.R2Upload) {
	r.u.files.Release(up.ID)
	r.u.releaseDrop(ctx, up.Owner, up.Size)
}

// expire ends an upload that is still being sent: its parts are discarded.
func (r *R2) expire(ctx context.Context, up *store.R2Upload, why string) {
	if ok, err := r.store.MoveR2Upload(ctx, up.ID, store.R2Uploading, store.R2Expired); err != nil || !ok {
		return
	}
	if err := r.client.AbortMultipartUpload(ctx, up.ObjectKey, up.UploadID); err != nil {
		slog.Warn("r2 abort", "upload", up.ID, "err", err)
	}
	_ = r.store.FinishR2Upload(ctx, up.ID, store.R2Expired, "", why)
	r.release(ctx, up)
}

// fail ends an upload whose object exists (or may): the object is deleted.
func (r *R2) fail(ctx context.Context, up *store.R2Upload, why string) {
	if err := r.client.Delete(ctx, up.ObjectKey); err != nil {
		slog.Warn("r2 delete", "upload", up.ID, "err", err)
	}
	_ = r.client.AbortMultipartUpload(ctx, up.ObjectKey, up.UploadID)
	_ = r.store.FinishR2Upload(ctx, up.ID, store.R2Failed, "", why)
	r.release(ctx, up)
}

// restoreReservations reserves the quota of the uploads in progress when
// the server last stopped, and puts interrupted pulls back in line.
func (r *R2) restoreReservations() {
	ctx := context.Background()
	pending, err := r.store.R2UploadsPending(ctx)
	if err != nil {
		slog.Error("r2 uploads", "err", err)
		return
	}
	for _, up := range pending {
		r.u.files.RestoreReservation(up.ID, up.UserID, up.Size)
		if up.State == store.R2Pulling {
			_, _ = r.store.MoveR2Upload(ctx, up.ID, store.R2Pulling, store.R2Uploaded)
		}
	}
}

// Run pulls completed uploads from the bucket until ctx is done.
func (r *R2) Run(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		ups, err := r.store.R2UploadsIn(ctx, store.R2Uploaded, 20)
		if err != nil && ctx.Err() == nil {
			slog.Error("r2 uploads", "err", err)
		}
		for _, up := range ups {
			if ctx.Err() != nil {
				return
			}
			r.pullOne(ctx, up)
		}
		if len(ups) == 20 {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.wake:
		}
	}
}

func (r *R2) pullOne(ctx context.Context, up *store.R2Upload) {
	if ok, err := r.store.MoveR2Upload(ctx, up.ID, store.R2Uploaded, store.R2Pulling); err != nil || !ok {
		return
	}
	final, err := r.pull(ctx, up)
	bg := context.WithoutCancel(ctx)
	var refused refusal
	switch {
	case err == nil:
		_ = r.store.FinishR2Upload(bg, up.ID, store.R2Done, final, "")
		r.u.files.Release(up.ID)
		if err := r.client.Delete(bg, up.ObjectKey); err != nil {
			slog.Warn("r2 delete after pull", "upload", up.ID, "err", err) // the bucket's lifecycle rule is the backstop
		}
	case ctx.Err() != nil:
		// Shutting down: the next start takes it up again.
		_, _ = r.store.MoveR2Upload(bg, up.ID, store.R2Pulling, store.R2Uploaded)
	case errors.As(err, &refused):
		r.fail(bg, up, refused.Error())
	case up.Attempts+1 >= r2PullAttempts:
		slog.Error("r2 pull failed for good", "upload", up.ID, "err", err)
		r.fail(bg, up, "the file could not be fetched from the upload buffer")
	default:
		slog.Warn("r2 pull", "upload", up.ID, "attempt", up.Attempts+1, "err", err)
		_ = r.store.RetryR2Upload(bg, up.ID, err.Error())
	}
}

// refusal is a reason a fetched file is not kept, to tell its uploader.
type refusal string

func (e refusal) Error() string { return string(e) }

// pull fetches the object of up into staging, checks it and places it.
func (r *R2) pull(ctx context.Context, up *store.R2Upload) (string, error) {
	owner, err := r.store.UserByID(ctx, up.UserID)
	if err != nil {
		return "", refusal("the account no longer exists")
	}
	if owner.Disabled {
		return "", refusal("the account is disabled")
	}
	access, err := files.AccessFor(ctx, r.store, owner)
	if err != nil {
		return "", err
	}
	dir, err := r.u.files.FS.Authorize(access, up.DestDir, files.PermWrite)
	if err != nil {
		return "", refusal("the destination is no longer writable")
	}
	var share *store.Share
	if id, ok := ownerShare(up.Owner); ok {
		if share, err = r.store.ShareByID(ctx, id); err != nil {
			return "", refusal("the link no longer exists")
		}
	}
	var parts []r2.Part
	if err := json.Unmarshal(up.Parts, &parts); err != nil {
		return "", err
	}
	stage, _ := r.u.files.FS.ReservedDir(dir, "uploads")
	staged := stage + "/r2-" + up.ID
	root := r.u.files.FS.Root()
	f, err := root.OpenFile(staged, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o640)
	if err != nil {
		return "", err
	}
	placed := false
	defer func() {
		if !placed {
			_ = root.Remove(staged)
		}
	}()
	body, _, err := r.client.Get(ctx, up.ObjectKey)
	if err != nil {
		_ = f.Close()
		return "", err
	}
	err = r.copyChecked(f, body, up, parts)
	_ = body.Close()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	// A drop link's kinds of file, by what was received.
	if share != nil {
		if lim := r.u.shares.Limits(share); len(lim.Types) > 0 {
			sf, err := root.Open(staged)
			if err != nil {
				return "", err
			}
			t, err := files.VerifyFile(up.Name, sf, up.Size)
			_ = sf.Close()
			if err != nil || !lim.Allows(t.Mime) {
				return "", refusal("this link does not accept this kind of file")
			}
		}
	}
	final, err := r.u.moveIn(staged, dir, up.Name)
	if err != nil {
		return "", err
	}
	placed = true
	r.u.files.AddUsage(up.UserID, up.Size)
	details := map[string]any{"size": up.Size, "via": "r2"}
	action := audit.FileUpload
	if share != nil {
		action, details["share_id"] = audit.ShareUpload, share.ID
	}
	r.u.audit.LogUser(context.WithoutCancel(ctx), &up.UserID, action, final, details)
	return final, nil
}

// copyChecked copies the object to w, holding it to the size announced and,
// part by part, to the MD5 sums the bucket reported for the parts.
func (r *R2) copyChecked(w io.Writer, body io.Reader, up *store.R2Upload, parts []r2.Part) error {
	var total int64
	for _, p := range parts {
		var h hash.Hash = md5.New() // #nosec G401 -- see the import // nosemgrep: go.lang.security.audit.crypto.use_of_weak_crypto.use-of-md5
		n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(body, p.Size))
		total += n
		if err != nil {
			return err
		}
		if n != p.Size {
			return fmt.Errorf("part %d is %d bytes, want %d", p.Number, n, p.Size)
		}
		if r.cfg.VerifyMD5 && len(p.ETag) == 32 && hex.EncodeToString(h.Sum(nil)) != p.ETag {
			return fmt.Errorf("part %d does not match its checksum", p.Number)
		}
	}
	// Nothing may follow the last part.
	if n, _ := io.Copy(io.Discard, io.LimitReader(body, 1)); n != 0 || total != up.Size {
		return fmt.Errorf("the object is not %d bytes", up.Size)
	}
	return nil
}

// Cleanup ends uploads that have stood still (sooner on drop links, whose
// quota is held on a stranger's word) and forgets old ones.
func (r *R2) Cleanup(ctx context.Context, maxAge time.Duration) {
	ups, err := r.store.R2UploadsIn(ctx, store.R2Uploading, 1000)
	if err != nil {
		slog.Error("r2 uploads", "err", err)
		return
	}
	for _, up := range ups {
		age := maxAge
		if _, drop := ownerShare(up.Owner); drop {
			age = min(age, dropStale)
		}
		if time.Since(up.UpdatedAt) > age {
			r.expire(ctx, up, "the upload was not continued")
		}
	}
	if err := r.store.DeleteR2UploadsBefore(ctx, time.Now().AddDate(0, 0, -7)); err != nil {
		slog.Error("r2 uploads", "err", err)
	}
}
