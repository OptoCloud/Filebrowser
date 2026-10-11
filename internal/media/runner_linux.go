package media

import (
	"os"
	"os/exec"
	"syscall"
)

// inputSpec passes the opened file as fd 3, read through ffmpeg's fd:
// protocol (seekable for regular files). The sandbox needs no /proc, which
// unprivileged LXC containers often cannot mount in a nested namespace.
func inputSpec(in *Input) (string, []*os.File, *os.File) {
	return fdInput, []*os.File{in.File}, nil
}

const inputFD = "3"

func setProcAttrs(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	// Kill the whole process group on cancellation.
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
}
