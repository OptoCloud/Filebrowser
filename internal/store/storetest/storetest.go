// Package storetest provides a fresh PostgreSQL database per test.
package storetest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"filebrowser/internal/auth"
	"filebrowser/internal/store"

	"github.com/jackc/pgx/v5"
)

// New creates an empty, migrated database and drops it after the test.
// It skips the test unless FB_TEST_DATABASE_URL points at a server where the
// user may CREATE DATABASE.
func New(t testing.TB) *store.Store {
	t.Helper()
	base := os.Getenv("FB_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("FB_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	name := "fbtest_" + strings.ToLower(strings.NewReplacer("-", "", "_", "").Replace(auth.NewToken(9)))
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database: %v", err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	st, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		st.Close()
		_, err := admin.Exec(ctx, fmt.Sprintf("DROP DATABASE %s WITH (FORCE)", name))
		if err != nil {
			t.Logf("drop database: %v", err)
		}
		_ = admin.Close(ctx)
	})
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return st
}
