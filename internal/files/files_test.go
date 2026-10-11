package files

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filebrowser/internal/store"
	"filebrowser/internal/store/storetest"
)

func TestCleanRejectsTraversal(t *testing.T) {
	bad := []string{"..", "../etc/passwd", "a/../../b", "a/..", `a\..\b`, "\x00", "a/\x00/b",
		".filebrowser", "/.filebrowser/trash", "a/.filebrowser", "a/.filebrowser/trash/x", "..%2f"}
	for _, p := range bad {
		if p == "..%2f" {
			continue // URL decoding happens before Clean; covered by server tests
		}
		if _, err := Clean(p); err == nil {
			t.Errorf("Clean(%q) succeeded", p)
		}
	}
	good := map[string]string{"": "", "/": "", "a": "a", "/a/b/": "a/b", "a//b/./c": "a/b/c", "a/.filebrowserx": "a/.filebrowserx"}
	for in, want := range good {
		got, err := Clean(in)
		if err != nil || got != want {
			t.Errorf("Clean(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestValidName(t *testing.T) {
	for _, n := range []string{"", ".", "..", "a/b", `a\b`, "x\x00", "tab\tname", "  ", Reserved} {
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
	if err := f.Move("home/docs", "home/a.txt"); !errors.Is(err, ErrExists) {
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

func TestReservedHiddenAtAnyDepth(t *testing.T) {
	f, dir := newFS(t)
	os.MkdirAll(filepath.Join(dir, "share", Reserved, "trash"), 0o755)
	os.WriteFile(filepath.Join(dir, "share", Reserved, "trash", "x"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "share", "song.flac"), nil, 0o644)
	admin := &Access{Admin: true}
	entries, _, err := f.List(admin, "share")
	if err != nil || len(entries) != 1 || entries[0].Name != "song.flac" {
		t.Fatalf("List = %+v, %v", entries, err)
	}
	if res, _ := f.Search(context.Background(), admin, "", "x", 100, time.Second); len(res) != 0 {
		t.Fatalf("search found reserved entries: %+v", res)
	}
	if err := os.Symlink(filepath.Join(Reserved, "trash"), filepath.Join(dir, "share", "peek")); err == nil {
		if _, err := f.Resolve("share/peek/x"); !errors.Is(err, ErrForbidden) {
			t.Fatalf("Resolve through link into reserved dir: %v", err)
		}
	}
	if n, _ := f.Size("share"); n != 0 {
		t.Fatalf("Size counted reserved entries: %d", n)
	}
}

// crossDevice makes renames between different top-level directories fail as
// they would between filesystems; with layoutKnown false every cross-device
// fallback rename fails too, as on a platform without device ids.
func crossDevice(f *FS, layoutKnown bool) {
	real := f.rename
	f.rename = func(from, to string) error {
		top := func(p string) string { t, _, _ := strings.Cut(p, "/"); return t }
		if top(from) != top(to) && (!layoutKnown || top(from) != Reserved) {
			return &os.LinkError{Op: "rename", Old: from, New: to, Err: errCrossDevice}
		}
		return real(from, to)
	}
}

func TestMoveAcrossFilesystems(t *testing.T) {
	if errCrossDevice == nil {
		t.Skip("no cross-device error on this platform")
	}
	for _, known := range []bool{true, false} {
		f, dir := newFS(t)
		crossDevice(f, known)
		mtime := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
		os.MkdirAll(filepath.Join(dir, "a", "tree", "sub"), 0o755)
		os.MkdirAll(filepath.Join(dir, "b"), 0o755)
		os.WriteFile(filepath.Join(dir, "a", "tree", "sub", "f.txt"), []byte("hello"), 0o644)
		os.Chtimes(filepath.Join(dir, "a", "tree", "sub", "f.txt"), mtime, mtime)
		links := os.Symlink("sub/f.txt", filepath.Join(dir, "a", "tree", "link")) == nil

		if err := f.Move("a/tree", "b/tree"); err != nil {
			t.Fatalf("known=%v: Move: %v", known, err)
		}
		if _, err := os.Lstat(filepath.Join(dir, "a", "tree")); !os.IsNotExist(err) {
			t.Fatalf("known=%v: source still exists: %v", known, err)
		}
		got, err := os.ReadFile(filepath.Join(dir, "b", "tree", "sub", "f.txt"))
		if err != nil || string(got) != "hello" {
			t.Fatalf("known=%v: copied content = %q, %v", known, got, err)
		}
		if fi, _ := os.Stat(filepath.Join(dir, "b", "tree", "sub", "f.txt")); !fi.ModTime().Equal(mtime) {
			t.Fatalf("known=%v: mtime not preserved: %v", known, fi.ModTime())
		}
		if links {
			// Windows stores the target with its own separator.
			if target, err := os.Readlink(filepath.Join(dir, "b", "tree", "link")); err != nil || filepath.ToSlash(target) != "sub/f.txt" {
				t.Fatalf("known=%v: symlink = %q, %v", known, target, err)
			}
		}
		if des, _ := os.ReadDir(filepath.Join(dir, Reserved, "tmp")); len(des) != 0 {
			t.Fatalf("known=%v: temporary copies left behind: %v", known, des)
		}
		os.WriteFile(filepath.Join(dir, "a", "x"), nil, 0o644)
		if err := f.Move("a/x", "b/tree"); !errors.Is(err, ErrExists) {
			t.Fatalf("known=%v: move over existing: %v", known, err)
		}
	}
}

func TestDeleteRestoreAcrossFilesystems(t *testing.T) {
	if errCrossDevice == nil {
		t.Skip("no cross-device error on this platform")
	}
	f, dir := newFS(t)
	crossDevice(f, false)
	st := storetest.New(t)
	u, err := st.CreateUser(context.Background(), "u", "", "admin", 0)
	if err != nil {
		t.Fatal(err)
	}
	s := NewService(f, st)
	a := &Access{Admin: true}
	os.MkdirAll(filepath.Join(dir, "share"), 0o755)
	os.WriteFile(filepath.Join(dir, "share", "doc.txt"), []byte("d"), 0o644)
	items, err := s.Delete(context.Background(), u.ID, a, []string{"share/doc.txt"})
	if err != nil || len(items) != 1 {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Restore(context.Background(), u.ID, a, items[0].ID); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "share", "doc.txt")); string(got) != "d" {
		t.Fatalf("restored content = %q", got)
	}
}

// Case-insensitive filesystems and Windows resolve other spellings of the
// reserved directory to it (GHSA-fgm5-pw99-w2p7 class).
func TestReservedAliasesRejected(t *testing.T) {
	f, dir := newFS(t)
	os.WriteFile(filepath.Join(dir, Reserved, "trash", "x.txt"), []byte("trashed"), 0o644)
	for _, p := range []string{".FILEBROWSER/trash/x.txt", ".filebrowser./trash/x.txt", ".FileBrowser ./trash", "a/.FILEBROWSER"} {
		if _, err := Clean(p); err == nil {
			t.Errorf("Clean(%q) accepted", p)
		}
	}
	for _, n := range []string{".FILEBROWSER", ".filebrowser.", ".filebrowser "} {
		if ValidName(n) == nil {
			t.Errorf("ValidName(%q) accepted", n)
		}
	}
	// Windows 8.3 short name, where the filesystem generates them.
	short := "FILEBR~1"
	if _, err := os.Stat(filepath.Join(dir, short, "trash", "x.txt")); err != nil {
		t.Logf("no 8.3 alias on this filesystem: %v", err)
		return
	}
	admin := &Access{Admin: true}
	if _, err := f.Authorize(admin, short+"/trash/x.txt", PermRead); !errors.Is(err, ErrForbidden) {
		t.Errorf("Authorize through 8.3 alias: %v", err)
	}
	if _, err := f.AuthorizeEntry(admin, short, PermWrite); !errors.Is(err, ErrForbidden) {
		t.Errorf("AuthorizeEntry on 8.3 alias: %v", err)
	}
}

func TestFileIDFollowsTheFileNotThePath(t *testing.T) {
	f, dir := newFS(t)
	os.MkdirAll(filepath.Join(dir, "d"), 0o755)
	os.WriteFile(filepath.Join(dir, "d", "a.txt"), []byte("a"), 0o644)
	for _, p := range []string{"d", "d/a.txt"} {
		id, err := f.FileID(p)
		if err != nil {
			t.Fatalf("FileID(%q): %v", p, err)
		}
		if again, _ := f.FileID(p); again != id {
			t.Fatalf("FileID(%q) not stable: %q vs %q", p, id, again)
		}
	}
	before, _ := f.FileID("d/a.txt")
	if err := f.Move("d/a.txt", "d/b.txt"); err != nil {
		t.Fatal(err)
	}
	if after, _ := f.FileID("d/b.txt"); after != before {
		t.Errorf("rename changed the identity: %q vs %q", before, after)
	}
	os.WriteFile(filepath.Join(dir, "d", "a.txt"), []byte("new"), 0o644)
	if replaced, _ := f.FileID("d/a.txt"); before != "" && replaced == before {
		t.Errorf("a new file at the old path has the old identity %q", replaced)
	}
}
