package files

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRealMultipleFilesystems runs against a real layout: FB_TEST_MULTIFS_ROOT
// names a directory on one filesystem whose subdirectory "mnt" is a mount of
// another (for example a tmpfs). It is skipped otherwise.
func TestRealMultipleFilesystems(t *testing.T) {
	dir := os.Getenv("FB_TEST_MULTIFS_ROOT")
	if dir == "" {
		t.Skip("FB_TEST_MULTIFS_ROOT not set")
	}
	f, err := OpenFS(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	crossings := 0
	real := f.rename
	f.rename = func(from, to string) error {
		err := real(from, to)
		if isCrossDevice(err) {
			crossings++
		}
		return err
	}

	os.MkdirAll(filepath.Join(dir, "mnt", "share", "sub"), 0o755)
	if got, known := f.ReservedDir("mnt/share/sub", "trash"); got != "mnt/"+Reserved+"/trash" || !known {
		t.Fatalf("ReservedDir on the mount = %q, %v", got, known)
	}
	if got, known := f.ReservedDir("local", "trash"); got != Reserved+"/trash" || !known {
		t.Fatalf("ReservedDir on files_root = %q, %v", got, known)
	}
	if got, _ := f.ReservedDir("mnt/share/not/yet/created", "uploads"); got != "mnt/"+Reserved+"/uploads" {
		t.Fatalf("ReservedDir for a missing dir = %q", got)
	}

	// Within the mount: trash is a rename.
	os.WriteFile(filepath.Join(dir, "mnt", "share", "doc.txt"), []byte("d"), 0o644)
	trash, _ := f.ReservedDir("mnt/share", "trash")
	if err := f.Move("mnt/share/doc.txt", trash+"/doc.txt"); err != nil || crossings != 0 {
		t.Fatalf("move to trash on the same filesystem: %v (crossings %d)", err, crossings)
	}
	// Between filesystems: copied.
	os.MkdirAll(filepath.Join(dir, "local", "tree"), 0o755)
	os.WriteFile(filepath.Join(dir, "local", "tree", "a.txt"), []byte("a"), 0o644)
	if err := f.Move("local/tree", "mnt/share/tree"); err != nil || crossings != 1 {
		t.Fatalf("move across filesystems: %v (crossings %d)", err, crossings)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "mnt", "share", "tree", "a.txt")); string(b) != "a" {
		t.Fatalf("copied content = %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "local", "tree")); !os.IsNotExist(err) {
		t.Fatalf("source remains: %v", err)
	}
}
