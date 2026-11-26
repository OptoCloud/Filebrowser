package media

import (
	"os"
	"os/exec"
	"testing"
)

// The filter lets an ordinary program run and fork, and refuses the calls
// that make namespaces or need a socket.
func TestSeccompFilter(t *testing.T) {
	if seccompProgram == nil {
		t.Skip("no filter for this architecture")
	}
	if len(seccompProgram)%8 != 0 {
		t.Fatalf("filter is %d bytes", len(seccompProgram))
	}
	run := func(filter bool, argv ...string) error {
		var extra []*os.File
		fd := -1
		if filter {
			f := seccompFile()
			if f == nil {
				t.Fatal("no filter file")
			}
			defer f.Close()
			extra, fd = []*os.File{f}, 3
		}
		args := append(bwrapArgs("", "", fd), argv...)
		cmd := exec.Command(args[0], args[1:]...)
		cmd.ExtraFiles = extra
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("%v: %v: %s", argv, err, out)
		}
		return err
	}
	if run(false, "/usr/bin/true") != nil {
		t.Skip("bubblewrap cannot run here")
	}
	if err := run(true, "/usr/bin/true"); err != nil {
		t.Fatalf("a plain program under the filter: %v", err)
	}
	if err := run(true, "/bin/sh", "-c", "(exit 0) & wait $!; ls /usr >/dev/null"); err != nil {
		t.Fatalf("forking under the filter: %v", err)
	}
	if _, err := os.Stat("/usr/bin/unshare"); err != nil {
		t.Skip("no unshare to try")
	}
	if run(false, "/usr/bin/unshare", "-U", "/usr/bin/true") != nil {
		t.Skip("nested user namespaces are not available here anyway")
	}
	if run(true, "/usr/bin/unshare", "-U", "/usr/bin/true") == nil {
		t.Fatal("a user namespace was made under the filter")
	}
	if run(true, "/usr/bin/unshare", "-m", "/usr/bin/true") == nil {
		t.Fatal("a mount namespace was made under the filter")
	}
}
