// Command filebrowser is a self-hosted file browser and sharer.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"filebrowser/internal/auth"
	"filebrowser/internal/config"
	"filebrowser/internal/files"
	"filebrowser/internal/media"
	"filebrowser/internal/sandbox"
	"filebrowser/internal/server"
	"filebrowser/internal/store"
	"filebrowser/web"
)

const usage = `usage: filebrowser [-config path] <command> [args]

commands:
  serve                                 run the server
  health                                check that the local server is up
  version                               print the version
  service install|uninstall             register as a Windows service
  migrate                               apply database migrations
  config-example                        print an example configuration
  user add <name> [-admin] [-grant path:read|write]... [-mfa-exempt] [-password-stdin]
  user list
  user reset-password <name> [-password-stdin]
  user reset-totp <name>                remove TOTP and all security keys
  user grant <name> <path> read|write|none
  user disable|enable <name>
  user mfa <name> required|exempt       require two-factor login, or exempt a
                                        non-admin user from it

The config path defaults to $FB_CONFIG or ./filebrowser.toml.
`

func main() {
	sandbox.MaybeRun()
	runAsInit()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	configPath := flag.String("config", envOr("FB_CONFIG", "filebrowser.toml"), "config file")
	flag.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	flag.Parse()
	setupServiceLogging()
	args := flag.Args()
	if len(args) == 0 {
		flag.Usage()
		os.Exit(2)
	}
	switch args[0] {
	case "config-example":
		fmt.Print(exampleConfig)
		return
	case "version":
		fmt.Println(version)
		return
	case "service":
		if err := serviceCmd(*configPath, args[1:]); err != nil {
			fatal(err)
		}
		return
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal(err)
	}
	if args[0] == "health" {
		if err := health(cfg); err != nil {
			fatal(err)
		}
		fmt.Println("ok")
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		fatal(err)
	}

	switch args[0] {
	case "serve":
		err = runServe(ctx, func(ctx context.Context) error { return serve(ctx, cfg, st) })
	case "migrate":
		fmt.Println("migrations applied")
	case "user":
		err = userCmd(ctx, cfg, st, args[1:])
	default:
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		fatal(err)
	}
}

// version is set at release build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

// health asks the running server (on cfg.Listen) for /api/health.
func health(cfg config.Config) error {
	host, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return err
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://"+net.JoinHostPort(host, port)+"/api/health", nil)
	if err != nil {
		return err
	}
	req.Host = cfg.AppHost()
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health: %s", resp.Status)
	}
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fatal(err error) {
	if isService() {
		slog.Error("fatal", "err", err) // stderr is not visible under the service manager
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

func serve(ctx context.Context, cfg config.Config, st *store.Store) error {
	keys, err := auth.LoadKeys(cfg.DataDir)
	if err != nil {
		return err
	}
	fsys, err := files.OpenFS(cfg.FilesRoot)
	if err != nil {
		return fmt.Errorf("open files_root: %w", err)
	}
	defer fsys.Close()
	fsvc := files.NewService(fsys, st)
	msvc, err := media.NewService(cfg.Media, st, fsys, cfg.FilesRoot, cfg.CacheDir)
	if err != nil {
		return err
	}
	if cfg.Media.Sandbox == "none" && !cfg.Dev {
		slog.Warn("media.sandbox is \"none\": ffmpeg runs unsandboxed; use \"bwrap\" in production")
	}
	if err := msvc.SelfTest(ctx); err != nil {
		if !cfg.Dev {
			return err
		}
		slog.Warn("media disabled in practice", "err", err)
	}
	srv, err := server.New(server.Deps{Config: cfg, Store: st, Keys: keys, Files: fsvc, Media: msvc, Web: web.FS()})
	if err != nil {
		return err
	}
	go srv.Janitor(ctx)

	hs := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    64 << 10,
		// No overall read/write timeouts: uploads and media streams are long-lived.
	}
	errc := make(chan error, 2)
	if cfg.MetricsListen != "" {
		ms := &http.Server{Addr: cfg.MetricsListen, Handler: srv.Metrics.Handler(version),
			ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 30 * time.Second}
		go func() {
			slog.Info("metrics", "addr", cfg.MetricsListen)
			errc <- ms.ListenAndServe()
		}()
		defer ms.Close()
	}
	go func() {
		slog.Info("listening", "addr", cfg.Listen, "app", cfg.AppOrigin, "share", cfg.ShareOrigin)
		errc <- hs.ListenAndServe()
	}()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return hs.Shutdown(shutdown)
}
