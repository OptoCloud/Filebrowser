package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
	"golang.org/x/sys/windows/svc/mgr"
)

const serviceName = "filebrowser"

// serviceAccount is a virtual account: a per-service identity with no
// password and no rights beyond what is granted to "NT SERVICE\filebrowser".
const serviceAccount = `NT SERVICE\filebrowser`

func isService() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

// setupServiceLogging sends logs to the Windows Event Log when running under
// the service manager, where stderr goes nowhere.
func setupServiceLogging() {
	if !isService() {
		return
	}
	el, err := eventlog.Open(serviceName)
	if err != nil {
		return
	}
	slog.SetDefault(slog.New(&eventlogHandler{el: el}))
}

// runServe runs serve directly, or under the service manager when started
// as a Windows service.
func runServe(ctx context.Context, serve func(context.Context) error) error {
	if !isService() {
		return serve(ctx)
	}
	var serveErr error
	err := svc.Run(serviceName, &handler{run: func(c context.Context) error {
		serveErr = serve(c)
		return serveErr
	}})
	if err != nil {
		return err
	}
	return serveErr
}

type handler struct{ run func(context.Context) error }

func (h *handler) Execute(_ []string, req <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- h.run(ctx) }()
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-errc:
			if err != nil {
				slog.Error("server stopped", "err", err)
				return true, 1
			}
			return false, 0
		case c := <-req:
			switch c.Cmd {
			case svc.Interrogate:
				status <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending, WaitHint: 20000}
				cancel()
			}
		}
	}
}

// serviceCmd implements "service install|uninstall".
func serviceCmd(configPath string, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: filebrowser -config <path> service install|uninstall")
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("%w (run from an elevated prompt)", err)
	}
	defer func() { _ = m.Disconnect() }()
	switch args[0] {
	case "install":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		cfg, err := filepath.Abs(configPath)
		if err != nil {
			return err
		}
		if _, err := os.Stat(cfg); err != nil {
			return fmt.Errorf("config: %w", err)
		}
		s, err := m.CreateService(serviceName, exe, mgr.Config{
			DisplayName:      "filebrowser",
			Description:      "Self-hosted file browser and sharer",
			StartType:        mgr.StartAutomatic,
			DelayedAutoStart: true, // after PostgreSQL
			ServiceStartName: serviceAccount,
		}, "-config", cfg, "serve")
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()
		restart := []mgr.RecoveryAction{{Type: mgr.ServiceRestart, Delay: 5 * time.Second}, {Type: mgr.ServiceRestart, Delay: 30 * time.Second}}
		if err := s.SetRecoveryActions(restart, 86400); err != nil {
			return err
		}
		if err := eventlog.InstallAsEventCreate(serviceName, eventlog.Error|eventlog.Warning|eventlog.Info); err != nil &&
			!strings.Contains(err.Error(), "exists") {
			return err
		}
		fmt.Printf(`Installed service %q running as %s.
Grant it access, then start it:
  icacls "<files_root>" /grant "%s:(OI)(CI)M"
  icacls "<data_dir>" /grant "%s:(OI)(CI)M"
  icacls "<cache_dir>" /grant "%s:(OI)(CI)M"
  icacls %q /grant "%s:R"
  Start-Service filebrowser
`, serviceName, serviceAccount, serviceAccount, serviceAccount, serviceAccount, cfg, serviceAccount)
		return nil
	case "uninstall":
		s, err := m.OpenService(serviceName)
		if err != nil {
			return err
		}
		defer func() { _ = s.Close() }()
		_, _ = s.Control(svc.Stop)
		if err := s.Delete(); err != nil {
			return err
		}
		_ = eventlog.Remove(serviceName)
		fmt.Println("Removed service", serviceName)
		return nil
	}
	return fmt.Errorf("unknown service command %q", args[0])
}

// eventlogHandler writes records as "message key=value ..." to the Event Log
// at the matching level.
type eventlogHandler struct {
	el    *eventlog.Log
	attrs []slog.Attr
}

func (e *eventlogHandler) Enabled(ctx context.Context, l slog.Level) bool { return l >= slog.LevelInfo }

func (e *eventlogHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder
	b.WriteString(r.Message)
	for _, a := range e.attrs {
		b.WriteString(" " + a.String())
	}
	r.Attrs(func(a slog.Attr) bool {
		b.WriteString(" " + a.String())
		return true
	})
	switch {
	case r.Level >= slog.LevelError:
		return e.el.Error(1, b.String())
	case r.Level >= slog.LevelWarn:
		return e.el.Warning(1, b.String())
	}
	return e.el.Info(1, b.String())
}

func (e *eventlogHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &eventlogHandler{el: e.el, attrs: append(append([]slog.Attr{}, e.attrs...), as...)}
}

func (e *eventlogHandler) WithGroup(string) slog.Handler { return e }
