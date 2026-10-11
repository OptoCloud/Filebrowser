package server

import (
	"encoding/base32"
	"flag"
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"
)

var base32NoPad = base32.StdEncoding.WithPadding(base32.NoPadding)

func strconvFormat(n int64) string { return strconv.FormatInt(n, 10) }

func TestMain(m *testing.M) {
	flag.Parse()
	if !testing.Verbose() {
		slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	}
	os.Exit(m.Run())
}
