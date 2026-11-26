package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"golang.org/x/term"

	"filebrowser/internal/audit"
	"filebrowser/internal/auth"
	"filebrowser/internal/config"
	"filebrowser/internal/files"
	"filebrowser/internal/server"
	"filebrowser/internal/store"
)

// The "user" command: managing accounts from the server's own shell, which
// is how the first administrator is made and how a locked-out one gets back in.

// userCLI is what every user subcommand works with.
type userCLI struct {
	ctx context.Context
	cfg config.Config
	st  *store.Store
	al  *audit.Logger
}

func userCmd(ctx context.Context, cfg config.Config, st *store.Store, args []string) error {
	if len(args) == 0 {
		return errors.New("missing user subcommand")
	}
	c := &userCLI{ctx: ctx, cfg: cfg, st: st, al: &audit.Logger{Store: st}}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return c.list()
	case "add":
		return c.add(rest)
	case "reset-password":
		return c.resetPassword(rest)
	case "reset-totp":
		return c.resetTOTP(rest)
	case "grant":
		return c.grant(rest)
	case "mfa":
		return c.mfa(rest)
	case "disable", "enable":
		return c.setDisabled(sub, rest)
	}
	return fmt.Errorf("unknown user subcommand %q", sub)
}

// log records what was done from the command line in the audit log.
func (c *userCLI) log(action, target string, details any) {
	c.al.LogUser(c.ctx, nil, audit.CLI, target, map[string]any{"command": action, "details": details})
}

// named parses the flags of a subcommand that takes one user name and
// looks that user up.
func (c *userCLI) named(fs *flag.FlagSet, args []string) (*store.User, error) {
	name, err := positional(fs, args)
	if err != nil {
		return nil, err
	}
	return c.st.UserByName(c.ctx, name)
}

// newPasswordHash reads a password for the account name, holds it to the
// password policy and hashes it.
func (c *userCLI) newPasswordHash(fromStdin bool, name string) (string, error) {
	pw, err := readPassword(fromStdin)
	if err != nil {
		return "", err
	}
	if err := (&auth.PasswordPolicy{Pwned: c.cfg.CheckPwnedPasswords}).Check(c.ctx, pw, name); err != nil {
		return "", err
	}
	return auth.HashPassword(pw)
}

// endSessions logs the user out everywhere and says how many sessions that were.
func (c *userCLI) endSessions(u *store.User) int64 {
	n, _ := c.st.DeleteUserSessions(c.ctx, u.ID, nil)
	return n
}

const (
	permRead  = "read"
	permWrite = "write"
	// permNone, for "user grant", takes a grant away.
	permNone = "none"
)

// parseGrant reads a grant written as path:read or path:write.
func parseGrant(g string) (store.Grant, error) {
	i := strings.LastIndex(g, ":")
	if i < 0 {
		return store.Grant{}, fmt.Errorf("invalid grant %q (want path:read or path:write)", g)
	}
	p, perm := g[:i], g[i+1:]
	cp, err := files.Clean(p)
	if err != nil || (perm != permRead && perm != permWrite) {
		return store.Grant{}, fmt.Errorf("invalid grant %q (want path:read or path:write)", g)
	}
	return store.Grant{Path: cp, Permission: perm}, nil
}

func (c *userCLI) list() error {
	users, err := c.st.ListUsers(c.ctx)
	if err != nil {
		return err
	}
	for _, u := range users {
		g, _ := c.st.Grants(c.ctx, u.ID)
		flags := ""
		if u.Disabled {
			flags += " disabled"
		}
		if u.TOTPEnabled() {
			flags += " totp"
		}
		if u.MFAExempt {
			flags += " mfa-exempt"
		}
		fmt.Printf("%-20s %-6s%s grants=%v\n", u.Username, u.Role, flags, g)
	}
	return nil
}

func (c *userCLI) add(args []string) error {
	fs := flag.NewFlagSet("user add", flag.ExitOnError)
	admin := fs.Bool("admin", false, "make the user an admin")
	stdin := fs.Bool("password-stdin", false, "read the password from stdin")
	exempt := fs.Bool("mfa-exempt", false, "do not require two-factor authentication (not for admins)")
	var grants multiFlag
	fs.Var(&grants, "grant", "grant path:read|write (repeatable)")
	name, err := positional(fs, args)
	if err != nil {
		return err
	}
	if *exempt && *admin {
		return errors.New("admins always need two-factor authentication; -mfa-exempt is for other users")
	}
	if !server.ValidUsername(name) {
		return errors.New("invalid username")
	}
	var gs []store.Grant
	for _, g := range grants {
		grant, err := parseGrant(g)
		if err != nil {
			return err
		}
		gs = append(gs, grant)
	}
	hash, err := c.newPasswordHash(*stdin, name)
	if err != nil {
		return err
	}
	role := "user"
	if *admin {
		role = "admin"
	}
	u, err := c.st.CreateUser(c.ctx, name, hash, role, 0)
	if err != nil {
		return err
	}
	if err := c.st.SetGrants(c.ctx, u.ID, gs); err != nil {
		return err
	}
	if *exempt {
		if err := c.st.UpdateUser(c.ctx, u.ID, store.UserUpdate{MFAExempt: exempt}); err != nil {
			return err
		}
	}
	c.log("user add", name, map[string]any{"role": role, "grants": gs, "mfa_exempt": *exempt})
	fmt.Printf("created %s (%s)\n", name, role)
	writes := slices.ContainsFunc(gs, func(g store.Grant) bool { return g.Permission == permWrite })
	if *admin || !*exempt && writes {
		fmt.Println("note: two-factor authentication will be required at first login")
	}
	return nil
}

func (c *userCLI) resetPassword(args []string) error {
	fs := flag.NewFlagSet("user reset-password", flag.ExitOnError)
	stdin := fs.Bool("password-stdin", false, "read the password from stdin")
	mustChange := fs.Bool("must-change", false, "make the user choose a new password at the next login")
	u, err := c.named(fs, args)
	if err != nil {
		return err
	}
	hash, err := c.newPasswordHash(*stdin, u.Username)
	if err != nil {
		return err
	}
	if err := c.st.SetPassword(c.ctx, u.ID, hash, *mustChange); err != nil {
		return err
	}
	n := c.endSessions(u)
	c.log("user reset-password", u.Username, nil)
	fmt.Printf("password reset for %s; %d sessions revoked\n", u.Username, n)
	return nil
}

func (c *userCLI) resetTOTP(args []string) error {
	u, err := c.named(flag.NewFlagSet("user reset-totp", flag.ExitOnError), args)
	if err != nil {
		return err
	}
	if err := c.st.ResetSecondFactors(c.ctx, u.ID); err != nil {
		return err
	}
	n := c.endSessions(u)
	c.log("user reset-totp", u.Username, nil)
	fmt.Printf("two-factor reset for %s; %d sessions revoked\n", u.Username, n)
	return nil
}

// grant sets, changes or (with "none") removes one grant of a user.
func (c *userCLI) grant(args []string) error {
	if len(args) != 3 {
		return errors.New("usage: user grant <name> <path> read|write|none")
	}
	name, perm := args[0], args[2]
	if perm != permRead && perm != permWrite && perm != permNone {
		return errors.New("permission must be read, write or none")
	}
	u, err := c.st.UserByName(c.ctx, name)
	if err != nil {
		return err
	}
	p, err := files.Clean(args[1])
	if err != nil {
		return err
	}
	gs, err := c.st.Grants(c.ctx, u.ID)
	if err != nil {
		return err
	}
	out := slices.DeleteFunc(gs, func(g store.Grant) bool { return g.Path == p })
	if perm != permNone {
		out = append(out, store.Grant{Path: p, Permission: perm})
	}
	if err := c.st.SetGrants(c.ctx, u.ID, out); err != nil {
		return err
	}
	c.log("user grant", name, map[string]any{"path": p, "permission": perm})
	fmt.Printf("grants for %s: %v\n", name, out)
	return nil
}

func (c *userCLI) mfa(args []string) error {
	if len(args) != 2 || (args[1] != "required" && args[1] != "exempt") {
		return errors.New("usage: user mfa <name> required|exempt")
	}
	u, err := c.st.UserByName(c.ctx, args[0])
	if err != nil {
		return err
	}
	exempt := args[1] == "exempt"
	if exempt && u.IsAdmin() {
		return errors.New("admins always need two-factor authentication")
	}
	if err := c.st.UpdateUser(c.ctx, u.ID, store.UserUpdate{MFAExempt: &exempt}); err != nil {
		return err
	}
	c.log("user mfa", args[0], map[string]any{"mfa_exempt": exempt})
	fmt.Printf("two-factor authentication for %s: %s\n", args[0], args[1])
	return nil
}

// setDisabled is "user disable" and "user enable"; a disabled user is logged out.
func (c *userCLI) setDisabled(sub string, args []string) error {
	u, err := c.named(flag.NewFlagSet("user "+sub, flag.ExitOnError), args)
	if err != nil {
		return err
	}
	disabled := sub == "disable"
	if err := c.st.UpdateUser(c.ctx, u.ID, store.UserUpdate{Disabled: &disabled}); err != nil {
		return err
	}
	if disabled {
		c.endSessions(u)
	}
	c.log("user "+sub, u.Username, nil)
	fmt.Printf("%sd %s\n", sub, u.Username)
	return nil
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

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

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
