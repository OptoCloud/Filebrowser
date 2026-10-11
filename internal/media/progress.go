package media

import (
	"bufio"
	"bytes"
	"io"
	"strconv"
	"strings"
)

type bytesBuffer = bytes.Buffer

// trackProgress reads ffmpeg "-progress" output and reports completion 0..1.
func trackProgress(r io.Reader, duration float64, report func(float64)) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok || duration <= 0 {
			continue
		}
		// out_time_us (and the misnamed out_time_ms) are microseconds.
		if k == "out_time_us" || k == "out_time_ms" {
			us, err := strconv.ParseFloat(v, 64)
			if err == nil && us > 0 {
				report(min(us/1e6/duration, 0.99))
			}
		}
	}
	_, _ = io.Copy(io.Discard, r)
}
