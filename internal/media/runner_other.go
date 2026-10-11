//go:build !linux && !windows

package media

import (
	"os"
	"os/exec"
)

// inputFD is unused: other systems (development only) pass the path.
const inputFD = ""

func inputSpec(in *Input) (string, []*os.File, *os.File) {
	return in.Path, nil, nil
}

func setProcAttrs(cmd *exec.Cmd) {}
