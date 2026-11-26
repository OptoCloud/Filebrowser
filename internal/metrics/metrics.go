// Package metrics counts what the server does and writes the counts in the
// Prometheus text format. It is a few counters behind one mutex-free map,
// not a client library: the server has a handful of things worth counting.
package metrics

import (
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Registry holds named counters. The zero value is ready to use.
type Registry struct {
	counters sync.Map // series (name with labels) -> *atomic.Int64
	help     sync.Map // name -> help text
	start    time.Time
	once     sync.Once
}

// Describe sets the help text shown for a metric name.
func (r *Registry) Describe(name, help string) { r.help.Store(name, help) }

// Add adds n to the counter name with the given label pairs ("key", "value", ...).
func (r *Registry) Add(n int64, name string, labels ...string) {
	series := name
	if len(labels) >= 2 {
		parts := make([]string, 0, len(labels)/2)
		for i := 0; i+1 < len(labels); i += 2 {
			parts = append(parts, labels[i]+`="`+escape(labels[i+1])+`"`)
		}
		series += "{" + strings.Join(parts, ",") + "}"
	}
	c, ok := r.counters.Load(series)
	if !ok {
		c, _ = r.counters.LoadOrStore(series, new(atomic.Int64))
	}
	c.(*atomic.Int64).Add(n)
}

func escape(v string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(v)
}

// Write writes every counter, and a few facts about the process.
func (r *Registry) Write(w io.Writer, version string) {
	r.once.Do(func() {
		if r.start.IsZero() {
			r.start = time.Now()
		}
	})
	var series []string
	r.counters.Range(func(k, _ any) bool { series = append(series, k.(string)); return true })
	sort.Strings(series)
	last := ""
	for _, s := range series {
		name, _, _ := strings.Cut(s, "{")
		if name != last {
			if h, ok := r.help.Load(name); ok {
				fmt.Fprintf(w, "# HELP %s %s\n", name, h)
			}
			fmt.Fprintf(w, "# TYPE %s counter\n", name)
			last = name
		}
		c, _ := r.counters.Load(s)
		fmt.Fprintf(w, "%s %d\n", s, c.(*atomic.Int64).Load())
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Fprintf(w, "# TYPE filebrowser_build_info gauge\nfilebrowser_build_info{version=\"%s\",go=\"%s\"} 1\n", escape(version), runtime.Version())
	fmt.Fprintf(w, "# TYPE filebrowser_uptime_seconds gauge\nfilebrowser_uptime_seconds %d\n", int64(time.Since(r.start).Seconds()))
	fmt.Fprintf(w, "# TYPE go_goroutines gauge\ngo_goroutines %d\n", runtime.NumGoroutine())
	fmt.Fprintf(w, "# TYPE go_memstats_heap_alloc_bytes gauge\ngo_memstats_heap_alloc_bytes %d\n", m.HeapAlloc)
	fmt.Fprintf(w, "# TYPE go_memstats_sys_bytes gauge\ngo_memstats_sys_bytes %d\n", m.Sys)
}

// Handler serves the metrics at /metrics.
func (r *Registry) Handler(version string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		r.Write(w, version)
	})
	return mux
}
