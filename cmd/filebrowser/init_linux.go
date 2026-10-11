package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
)

// runAsInit makes filebrowser safe to run as PID 1 (for example as a
// container entrypoint without --init). The kernel reparents orphans, such as
// the init process bwrap creates inside each sandbox, to PID 1, and Go's
// os/exec only reaps the children it started. As PID 1 we therefore re-exec
// ourselves as a child, forward signals to it and reap every process that
// exits, then exit with the child's status. It returns only when not PID 1.
func runAsInit() {
	if os.Getpid() != 1 {
		return
	}
	sigs := make(chan os.Signal, 8)
	signal.Notify(sigs, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGUSR1, syscall.SIGUSR2)
	exe, err := os.Executable()
	if err != nil {
		exe = "/proc/self/exe"
	}
	// #nosec G204 G702 -- re-executes this same binary with its own arguments.
	child, err := os.StartProcess(exe, os.Args, &os.ProcAttr{
		Env:   os.Environ(),
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "error: starting server under built-in init:", err)
		os.Exit(1)
	}
	go func() {
		for s := range sigs {
			_ = child.Signal(s)
		}
	}()
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, 0, nil)
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "error: wait:", err)
			os.Exit(1)
		}
		if pid != child.Pid {
			continue // a reaped orphan
		}
		if ws.Signaled() {
			os.Exit(128 + int(ws.Signal()))
		}
		os.Exit(ws.ExitStatus())
	}
}
