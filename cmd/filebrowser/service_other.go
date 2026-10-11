//go:build !windows

package main

import (
	"context"
	"errors"
)

func isService() bool { return false }

func setupServiceLogging() {}

func runServe(ctx context.Context, serve func(context.Context) error) error { return serve(ctx) }

func serviceCmd(string, []string) error {
	return errors.New("service install is Windows-only; on Linux use deploy/filebrowser.service (systemd)")
}
