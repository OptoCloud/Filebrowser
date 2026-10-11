// Package sandbox is the Windows counterpart of bubblewrap: a launcher mode
// of the filebrowser binary that runs ffmpeg in an AppContainer (no network,
// no file access beyond what is granted) inside a Job Object (memory and CPU
// limits, no child processes, killed with the launcher).
//
// The media runner starts the current executable with EnvExec=1; MaybeRun,
// called first thing in main (and in TestMain), then acts as the launcher.
package sandbox

import "os"

// Environment variables understood by the launcher.
const (
	EnvExec  = "FB_SANDBOX_EXEC"  // "1" selects launcher mode
	EnvWrite = "FB_SANDBOX_WRITE" // the only writable directory
	EnvMem   = "FB_SANDBOX_MEM"   // job memory limit in bytes
	EnvCPU   = "FB_SANDBOX_CPU"   // job CPU time limit in seconds
)

// MaybeRun runs the launcher and exits if this process was started as one.
func MaybeRun() {
	if os.Getenv(EnvExec) == "1" {
		os.Exit(launch(os.Args[1:]))
	}
}
