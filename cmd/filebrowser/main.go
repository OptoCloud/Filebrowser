// Command filebrowser is a self-hosted file browser and sharer.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"

	"filebrowser/internal/audit"
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
  user add <name> [-admin] [-grant path:read|write]... [-password-stdin]
  user list
  user reset-password <name> [-password-stdin]
  user reset-totp <name>                remove TOTP and all security keys
  user grant <name> <path> read|write|none
  user disable|enable <name>

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
	errc := make(chan error, 1)
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

func readPassword(fromStdin bool) (string, error) {
	if fromStdin || !term.IsTerminal(int(os.Stdin.Fd())) {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	fmt.Fprint(os.Stderr, "Password: ")
	a, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprint(os.Stderr, "Repeat: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if string(a) != string(b) {
		return "", errors.New("passwords do not match")
	}
	return string(a), nil
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func userCmd(ctx context.Context, cfg config.Config, st *store.Store, args []string) error {
	if len(args) == 0 {
		return errors.New("missing user subcommand")
	}
	al := &audit.Logger{Store: st}
	logCLI := func(action, target string, details any) {
		al.LogUser(ctx, nil, audit.CLI, target, map[string]any{"command": action, "details": details})
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		users, err := st.ListUsers(ctx)
		if err != nil {
			return err
		}
		for _, u := range users {
			g, _ := st.Grants(ctx, u.ID)
			flags := ""
			if u.Disabled {
				flags += " disabled"
			}
			if u.TOTPEnabled() {
				flags += " totp"
			}
			fmt.Printf("%-20s %-6s%s grants=%v\n", u.Username, u.Role, flags, g)
		}
		return nil

	case "add":
		fs := flag.NewFlagSet("user add", flag.ExitOnError)
		admin := fs.Bool("admin", false, "make the user an admin")
		stdin := fs.Bool("password-stdin", false, "read the password from stdin")
		var grants multiFlag
		fs.Var(&grants, "grant", "grant path:read|write (repeatable)")
		name, err := positional(fs, rest)
		if err != nil {
			return err
		}
		if !server.ValidUsername(name) {
			return errors.New("invalid username")
		}
		var gs []store.Grant
		for _, g := range grants {
			i := strings.LastIndex(g, ":")
			if i < 0 {
				return fmt.Errorf("invalid grant %q (want path:read or path:write)", g)
			}
			p, perm := g[:i], g[i+1:]
			cp, err := files.Clean(p)
			if err != nil || (perm != "read" && perm != "write") {
				return fmt.Errorf("invalid grant %q (want path:read or path:write)", g)
			}
			gs = append(gs, store.Grant{Path: cp, Permission: perm})
		}
		pw, err := readPassword(*stdin)
		if err != nil {
			return err
		}
		if err := auth.ValidatePassword(pw); err != nil {
			return err
		}
		hash, err := auth.HashPassword(pw)
		if err != nil {
			return err
		}
		role := "user"
		if *admin {
			role = "admin"
		}
		u, err := st.CreateUser(ctx, name, hash, role, 0)
		if err != nil {
			return err
		}
		if err := st.SetGrants(ctx, u.ID, gs); err != nil {
			return err
		}
		logCLI("user add", name, map[string]any{"role": role, "grants": gs})
		fmt.Printf("created %s (%s)\n", name, role)
		if role == "admin" || slices.ContainsFunc(gs, func(g store.Grant) bool { return g.Permission == "write" }) {
			fmt.Println("note: two-factor authentication will be required at first login")
		}
		return nil

	case "reset-password":
		fs := flag.NewFlagSet("user reset-password", flag.ExitOnError)
		stdin := fs.Bool("password-stdin", false, "read the password from stdin")
		name, err := positional(fs, rest)
		if err != nil {
			return err
		}
		u, err := st.UserByName(ctx, name)
		if err != nil {
			return err
		}
		pw, err := readPassword(*stdin)
		if err != nil {
			return err
		}
		if err := auth.ValidatePassword(pw); err != nil {
			return err
		}
		hash, err := auth.HashPassword(pw)
		if err != nil {
			return err
		}
		if err := st.SetPassword(ctx, u.ID, hash); err != nil {
			return err
		}
		n, _ := st.DeleteUserSessions(ctx, u.ID, nil)
		logCLI("user reset-password", name, nil)
		fmt.Printf("password reset for %s; %d sessions revoked\n", name, n)
		return nil

	case "reset-totp":
		name, err := positional(flag.NewFlagSet("user reset-totp", flag.ExitOnError), rest)
		if err != nil {
			return err
		}
		u, err := st.UserByName(ctx, name)
		if err != nil {
			return err
		}
		if err := st.ResetSecondFactors(ctx, u.ID); err != nil {
			return err
		}
		n, _ := st.DeleteUserSessions(ctx, u.ID, nil)
		logCLI("user reset-totp", name, nil)
		fmt.Printf("two-factor reset for %s; %d sessions revoked\n", name, n)
		return nil

	case "grant":
		if len(rest) != 3 {
			return errors.New("usage: user grant <name> <path> read|write|none")
		}
		u, err := st.UserByName(ctx, rest[0])
		if err != nil {
			return err
		}
		p, err := files.Clean(rest[1])
		if err != nil {
			return err
		}
		gs, err := st.Grants(ctx, u.ID)
		if err != nil {
			return err
		}
		var out []store.Grant
		for _, g := range gs {
			if g.Path != p {
				out = append(out, g)
			}
		}
		switch rest[2] {
		case "read", "write":
			out = append(out, store.Grant{Path: p, Permission: rest[2]})
		case "none":
		default:
			return errors.New("permission must be read, write or none")
		}
		if err := st.SetGrants(ctx, u.ID, out); err != nil {
			return err
		}
		logCLI("user grant", rest[0], map[string]any{"path": p, "permission": rest[2]})
		fmt.Printf("grants for %s: %v\n", rest[0], out)
		return nil

	case "disable", "enable":
		name, err := positional(flag.NewFlagSet("user "+sub, flag.ExitOnError), rest)
		if err != nil {
			return err
		}
		u, err := st.UserByName(ctx, name)
		if err != nil {
			return err
		}
		d := sub == "disable"
		if err := st.UpdateUser(ctx, u.ID, store.UserUpdate{Disabled: &d}); err != nil {
			return err
		}
		if d {
			_, _ = st.DeleteUserSessions(ctx, u.ID, nil)
		}
		logCLI("user "+sub, name, nil)
		fmt.Printf("%sd %s\n", sub, name)
		return nil
	}
	return fmt.Errorf("unknown user subcommand %q", sub)
}

// positional parses flags that may appear before or after one positional arg.
func positional(fs *flag.FlagSet, args []string) (string, error) {
	var pos []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		args = fs.Args()
		if len(args) > 0 {
			pos = append(pos, args[0])
			args = args[1:]
		}
	}
	if len(pos) != 1 {
		return "", errors.New("expected exactly one user name")
	}
	return pos[0], nil
}
