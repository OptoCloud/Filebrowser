package media

import (
	"os"
	"os/exec"
)

// inputFD is the descriptor ffmpeg reads the input from: the file is passed
// as stdin, an inherited handle, so the AppContainer needs no access to it.
const inputFD = "0"

func inputSpec(in *Input) (string, []*os.File, *os.File) {
	return fdInput, nil, in.File
}

func setProcAttrs(cmd *exec.Cmd) {}
