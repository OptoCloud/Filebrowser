package uploads

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"strings"

	tushandler "github.com/tus/tusd/v2/pkg/handler"

	"filebrowser/internal/files"
)

// rootStore is a tus data store confined to files_root through os.Root.
//
// The upload's data is staged in a Reserved "uploads" directory on the
// filesystem of its destination (chosen by the create hook, see
// files.FS.ReservedDir), so completing an upload is a rename even when
// files_root spans several filesystems. The .info files always live in the
// central files.UploadsDir, so uploads can be found by ID alone.
//
// Unlike tusd's filestore, no file is opened by absolute path, so a symlink
// planted in a staging directory cannot redirect writes outside files_root.
type rootStore struct {
	fs *files.FS
}

// storageDir is the FileInfo.Storage key holding the staging directory (a
// path relative to files_root).
const storageDir = "dir"

func (s rootStore) UseIn(c *tushandler.StoreComposer) {
	c.UseCore(s)
	c.UseTerminater(s)
}

func (s rootStore) NewUpload(_ context.Context, info tushandler.FileInfo) (tushandler.Upload, error) {
	if !validID(info.ID) {
		return nil, errors.New("uploads: invalid upload id")
	}
	dir := files.UploadsDir
	if d := info.Storage[storageDir]; d != "" {
		dir = d
	}
	if !stagingDir(dir) {
		return nil, errors.New("uploads: invalid staging directory")
	}
	info.Storage = map[string]string{"Type": "rootstore", storageDir: dir}
	u := &rootUpload{root: s.fs.Root(), info: info}
	f, err := u.root.OpenFile(u.binPath(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := u.writeInfo(); err != nil {
		_ = u.root.Remove(u.binPath())
		return nil, err
	}
	return u, nil
}

func (s rootStore) GetUpload(_ context.Context, id string) (tushandler.Upload, error) {
	if !validID(id) {
		return nil, tushandler.ErrNotFound
	}
	root := s.fs.Root()
	data, err := root.ReadFile(infoPath(id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, tushandler.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	var info tushandler.FileInfo
	if err := json.Unmarshal(data, &info); err != nil || info.ID != id || !stagingDir(info.Storage[storageDir]) {
		return nil, tushandler.ErrNotFound // corrupt, or written by an older storage backend
	}
	u := &rootUpload{root: root, info: info}
	fi, err := root.Stat(u.binPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, tushandler.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	u.info.Offset = fi.Size()
	return u, nil
}

func (s rootStore) AsTerminatableUpload(up tushandler.Upload) tushandler.TerminatableUpload {
	return up.(*rootUpload)
}

type rootUpload struct {
	root *os.Root
	info tushandler.FileInfo
}

// binPath is the staged data of info (relative to files_root).
func binPath(info tushandler.FileInfo) string { return info.Storage[storageDir] + "/" + info.ID }

func infoPath(id string) string { return files.UploadsDir + "/" + id + ".info" }

func (u *rootUpload) binPath() string { return binPath(u.info) }

func (u *rootUpload) writeInfo() error {
	data, err := json.Marshal(u.info)
	if err != nil {
		return err
	}
	return u.root.WriteFile(infoPath(u.info.ID), data, 0o640)
}

func (u *rootUpload) GetInfo(context.Context) (tushandler.FileInfo, error) { return u.info, nil }

func (u *rootUpload) WriteChunk(_ context.Context, _ int64, src io.Reader) (int64, error) {
	f, err := u.root.OpenFile(u.binPath(), os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, src)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	// A client that disconnects mid-chunk resumes from what was stored.
	if errors.Is(err, io.ErrUnexpectedEOF) {
		err = nil
	}
	u.info.Offset += n
	return n, err
}

func (u *rootUpload) GetReader(context.Context) (io.ReadCloser, error) {
	return u.root.Open(u.binPath())
}

func (u *rootUpload) FinishUpload(context.Context) error { return nil }

func (u *rootUpload) Terminate(context.Context) error {
	for _, p := range []string{u.binPath(), infoPath(u.info.ID)} {
		if err := u.root.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// validID accepts the IDs made by auth.NewToken (URL-safe base64).
func validID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// stagingDir reports whether dir is a Reserved uploads directory.
func stagingDir(dir string) bool {
	return dir == files.UploadsDir || strings.HasSuffix(dir, "/"+files.UploadsDir) && !strings.Contains(dir, "..")
}
