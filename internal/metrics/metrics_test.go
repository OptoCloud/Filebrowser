package metrics

import (
	"bytes"
	"strings"
	"testing"
)

func TestRegistry(t *testing.T) {
	var r Registry
	r.Describe("fb_requests_total", "Requests answered.")
	r.Add(1, "fb_requests_total", "origin", "app", "status", "2xx")
	r.Add(2, "fb_requests_total", "origin", "app", "status", "2xx")
	r.Add(1, "fb_requests_total", "origin", "share", "status", `4"x`)
	r.Add(5, "fb_bytes_total")
	var b bytes.Buffer
	r.Write(&b, "v1.2.3")
	out := b.String()
	for _, want := range []string{
		"# HELP fb_requests_total Requests answered.\n# TYPE fb_requests_total counter\n",
		`fb_requests_total{origin="app",status="2xx"} 3` + "\n",
		`fb_requests_total{origin="share",status="4\"x"} 1` + "\n",
		"fb_bytes_total 5\n", `filebrowser_build_info{version="v1.2.3"`, "go_goroutines ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "# TYPE fb_requests_total") != 1 {
		t.Error("a metric's type is written more than once")
	}
}
