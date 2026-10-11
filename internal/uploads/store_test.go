package uploads

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tushandler "github.com/tus/tusd/v2/pkg/handler"

	"filebrowser/internal/files"
)

func newStore(t *testing.T) (rootStore, string) {
	t.Helper()
	dir := t.TempDir()
	fsys, err := files.OpenFS(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { fsys.Close() })
	return rootStore{fs: fsys}, dir
}

func TestRootStoreLifecycle(t *testing.T) {
	s, dir := newStore(t)
	ctx := context.Background()
	os.MkdirAll(filepath.Join(dir, "share"), 0o755)
	stage, _ := s.fs.ReservedDir("share", "uploads")

	up, err := s.NewUpload(ctx, tushandler.FileInfo{ID: "abc_DEF-1", Size: 10, Storage: map[string]string{storageDir: stage}})
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []string{"hello", "world"} {
		if _, err := up.WriteChunk(ctx, 0, strings.NewReader(chunk)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetUpload(ctx, "abc_DEF-1")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := got.GetInfo(ctx)
	if info.Offset != 10 || info.Storage[storageDir] != stage {
		t.Fatalf("info = %+v", info)
	}
	data, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(binPath(info))))
	if string(data) != "helloworld" {
		t.Fatalf("staged data = %q", data)
	}
	if err := s.AsTerminatableUpload(got).Terminate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetUpload(ctx, "abc_DEF-1"); !errors.Is(err, tushandler.ErrNotFound) {
		t.Fatalf("after terminate: %v", err)
	}
}

func TestRootStoreRejectsBadIDsAndDirs(t *testing.T) {
	s, _ := newStore(t)
	ctx := context.Background()
	for _, id := range []string{"", "../x", "a/b", "a.info", strings.Repeat("a", 65)} {
		if _, err := s.NewUpload(ctx, tushandler.FileInfo{ID: id}); err == nil {
			t.Errorf("NewUpload accepted id %q", id)
		}
		if _, err := s.GetUpload(ctx, id); !errors.Is(err, tushandler.ErrNotFound) {
			t.Errorf("GetUpload(%q) = %v", id, err)
		}
	}
	for _, dir := range []string{"share", "../.filebrowser/uploads", "x/.filebrowser/trash", "/abs/.filebrowser/uploads/.."} {
		if _, err := s.NewUpload(ctx, tushandler.FileInfo{ID: "ok", Storage: map[string]string{storageDir: dir}}); err == nil {
			t.Errorf("NewUpload accepted staging dir %q", dir)
		}
	}
}
