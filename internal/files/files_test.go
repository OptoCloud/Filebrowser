package files

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"filebrowser/internal/store"
)

func TestCleanRejectsTraversal(t *testing.T) {
	bad := []string{"..", "../etc/passwd", "a/../../b", "a/..", `a\..\b`, "\x00", "a/\x00/b",
		".filebrowser", "/.filebrowser/trash", "..%2f"}
	for _, p := range bad {
		if p == "..%2f" {
			continue // URL decoding happens before Clean; covered by server tests
		}
		if _, err := Clean(p); err == nil {
			t.Errorf("Clean(%q) succeeded", p)
		}
	}
	good := map[string]string{"": "", "/": "", "a": "a", "/a/b/": "a/b", "a//b/./c": "a/b/c", "a/.filebrowser": "a/.filebrowser"}
	for in, want := range good {
		got, err := Clean(in)
		if err != nil || got != want {
			t.Errorf("Clean(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, n := range []string{"", ".", "..", "a/b", `a\b`, "x\x00", "tab\tname", "  "} {
		if ValidName(n) == nil {
			t.Errorf("ValidName(%q) accepted", n)
		}
	}
	for _, n := range []string{"a", "photo (1).jpg", ".hidden", "ünïcode"} {
		if err := ValidName(n); err != nil {
			t.Errorf("ValidName(%q): %v", n, err)
		}
	}
}

func TestAccess(t *testing.T) {
	a := &Access{Grants: []store.Grant{{Path: "media/music", Permission: "read"}, {Path: "users/alice", Permission: "write"}}}
	cases := map[string]Perm{
		"": PermTraverse, "media": PermTraverse, "media/music": PermRead, "media/music/x.flac": PermRead,
		"media/movies": PermNone, "media/musicx": PermNone, "users": PermTraverse, "users/alice/doc": PermWrite,
		"users/bob": PermNone,
	}
	for p, want := range cases {
		if got := a.Perm(p); got != want {
			t.Errorf("Perm(%q) = %v, want %v", p, got, want)
		}
	}
	if !a.IsGrantRoot("users/alice") || a.IsGrantRoot("users/alice/doc") {
		t.Error("IsGrantRoot wrong")
	}
}

func newFS(t *testing.T) (*FS, string) {
	dir := t.TempDir()
	f, err := OpenFS(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f, dir
}

func TestListFiltersByAccess(t *testing.T) {
	f, dir := newFS(t)
	for _, d := range []string{"media/music", "media/movies", "users/alice"} {
		os.MkdirAll(filepath.Join(dir, d), 0o755)
	}
	os.WriteFile(filepath.Join(dir, "media", "readme.txt"), []byte("x"), 0o644)
	a := &Access{Grants: []store.Grant{{Path: "media/music", Permission: "read"}}}

	root, perm, err := f.List(a, "")
	if err != nil || perm != PermTraverse || len(root) != 1 || root[0].Name != "media" {
		t.Fatalf("root listing = %+v %v %v", root, perm, err)
	}
	media, _, err := f.List(a, "media")
	if err != nil || len(media) != 1 || media[0].Name != "music" {
		t.Fatalf("media listing = %+v %v", media, err)
	}
	if _, _, err := f.List(a, "media/movies"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("movies: %v", err)
	}
	admin := &Access{Admin: true}
	all, _, _ := f.List(admin, "")
	for _, e := range all {
		if e.Name == Reserved {
			t.Fatal("reserved dir listed")
		}
	}
}

func TestSymlinkEscape(t *testing.T) {
	f, dir := newFS(t)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o644)
	if err := os.Symlink(outside, filepath.Join(dir, "escape")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if _, err := f.Open("escape/secret"); err == nil {
		t.Fatal("opened file through escaping symlink")
	}
	if _, _, err := f.OpenFile("escape/secret"); err == nil {
		t.Fatal("OpenFile followed escaping symlink")
	}
	entries, _, _ := f.List(&Access{Admin: true}, "")
	for _, e := range entries {
		if e.Name == "escape" {
			t.Fatal("escaping symlink listed")
		}
	}
}

func TestOpsAndUniquePath(t *testing.T) {
	f, dir := newFS(t)
	s := &Service{FS: f}
	a := &Access{Grants: []store.Grant{{Path: "home", Permission: "write"}, {Path: "ro", Permission: "read"}}}
	os.MkdirAll(filepath.Join(dir, "home"), 0o755)
	os.MkdirAll(filepath.Join(dir, "ro"), 0o755)
	if _, err := s.Mkdir(a, "home", "docs"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Mkdir(a, "ro", "x"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("mkdir in ro: %v", err)
	}
	if _, err := s.Mkdir(a, "", "top"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("mkdir at root: %v", err)
	}
	if _, err := s.Rename(a, "home", "h2"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("rename grant root: %v", err)
	}
	if _, err := s.Move(a, []string{"home/docs"}, "home/docs"); err == nil {
		t.Fatal("moved dir into itself")
	}
	os.WriteFile(filepath.Join(dir, "home", "a.txt"), nil, 0o644)
	p, _ := f.UniquePath("home/a.txt")
	if p != "home/a (1).txt" {
		t.Fatalf("UniquePath = %q", p)
	}
	if err := f.Rename("home/docs", "home/a.txt"); !errors.Is(err, ErrExists) {
		t.Fatalf("rename over existing: %v", err)
	}
}

func TestSearchRespectsAccess(t *testing.T) {
	f, dir := newFS(t)
	os.MkdirAll(filepath.Join(dir, "pub"), 0o755)
	os.MkdirAll(filepath.Join(dir, "priv"), 0o755)
	os.WriteFile(filepath.Join(dir, "pub", "song.flac"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "priv", "song2.flac"), nil, 0o644)
	a := &Access{Grants: []store.Grant{{Path: "pub", Permission: "read"}}}
	res, _ := f.Search(context.Background(), a, "", "song", 100, time.Second)
	if len(res) != 1 || res[0].Path != "pub/song.flac" {
		t.Fatalf("search = %+v", res)
	}
}

func TestResolveSymlinks(t *testing.T) {
	f, dir := newFS(t)
	os.MkdirAll(filepath.Join(dir, "pub", "sub"), 0o755)
	os.MkdirAll(filepath.Join(dir, "private"), 0o755)
	os.WriteFile(filepath.Join(dir, "private", "s.txt"), []byte("s"), 0o644)
	if err := os.Symlink(filepath.Join("..", "..", "private"), filepath.Join(dir, "pub", "sub", "up")); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	os.Symlink("loop", filepath.Join(dir, "pub", "loop"))
	os.Symlink(filepath.Join("..", "..", "outside"), filepath.Join(dir, "pub", "esc"))
	os.Symlink("sub", filepath.Join(dir, "pub", "alias"))

	if got, err := f.Resolve("pub/sub/up/s.txt"); err != nil || got != "private/s.txt" {
		t.Fatalf("Resolve = %q, %v", got, err)
	}
	if got, err := f.Resolve("pub/alias/new/file"); err != nil || got != "pub/sub/new/file" {
		t.Fatalf("Resolve with missing tail = %q, %v", got, err)
	}
	if _, err := f.Resolve("pub/loop"); err == nil {
		t.Fatal("symlink loop resolved")
	}
	if _, err := f.Resolve("pub/esc/x"); err == nil {
		t.Fatal("escaping link resolved")
	}
	a := &Access{Grants: []store.Grant{{Path: "pub", Permission: "write"}}}
	if _, err := f.Authorize(a, "pub/sub/up/s.txt", PermRead); err == nil {
		t.Fatal("authorized a link into another grant")
	}
	if got, err := f.Authorize(a, "pub/alias", PermRead); err != nil || got != "pub/sub" {
		t.Fatalf("in-grant alias: %q %v", got, err)
	}
	if got, err := f.AuthorizeEntry(a, "pub/sub/up", PermWrite); err != nil || got != "pub/sub/up" {
		t.Fatalf("entry op on the link itself: %q %v", got, err)
	}
}
