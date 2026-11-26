package r2

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Runs the whole round against a real S3-compatible server when one is
// given (FB_TEST_S3_ENDPOINT, _BUCKET, _KEY, _SECRET), e.g. MinIO in Docker.
func TestLiveRoundTrip(t *testing.T) {
	endpoint := os.Getenv("FB_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("FB_TEST_S3_ENDPOINT not set")
	}
	c, err := New(endpoint, os.Getenv("FB_TEST_S3_BUCKET"), Credentials{
		AccessKeyID: os.Getenv("FB_TEST_S3_KEY"), SecretAccessKey: os.Getenv("FB_TEST_S3_SECRET"), Region: os.Getenv("FB_TEST_S3_REGION")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := "test/odd name (1) é+~.bin"
	const partSize = 5 << 20
	data := bytes.Repeat([]byte("0123456789abcdef"), (partSize+1000)/16+1)[:partSize+1000]

	id, err := c.CreateMultipartUpload(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	put := func(n int, body []byte, declared int64) int {
		req, _ := http.NewRequest(http.MethodPut, c.PresignPart(key, id, n, declared, time.Minute), bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0 // a server may drop the connection on a body it will not take
		}
		defer res.Body.Close()
		io.Copy(io.Discard, res.Body)
		return res.StatusCode
	}
	// A body of another size than was signed for is refused.
	if code := put(1, data[:partSize-1], partSize); code == 200 {
		t.Fatal("a part of the wrong size was accepted")
	}
	if code := put(1, data[:partSize], partSize); code != 200 {
		t.Fatalf("part 1: %d", code)
	}
	if code := put(2, data[partSize:], 1000); code != 200 {
		t.Fatalf("part 2: %d", code)
	}
	parts, err := c.ListParts(ctx, key, id)
	if err != nil || len(parts) != 2 || parts[0].Size != partSize || parts[1].Size != 1000 {
		t.Fatalf("parts = %+v, %v", parts, err)
	}
	sum := md5.Sum(data[:partSize])
	if parts[0].ETag != hex.EncodeToString(sum[:]) {
		t.Errorf("part 1 ETag %s is not its MD5", parts[0].ETag)
	}
	if err := c.CompleteMultipartUpload(ctx, key, id, parts); err != nil {
		t.Fatal(err)
	}
	if n, err := c.Head(ctx, key); err != nil || n != int64(len(data)) {
		t.Fatalf("head = %d, %v", n, err)
	}
	rc, _, err := c.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, data) {
		t.Fatal("the object is not what was uploaded")
	}
	if err := c.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Head(ctx, key); err != ErrNotFound {
		t.Fatalf("head after delete: %v", err)
	}
	// Aborting discards an upload; doing it again is no error.
	id2, _ := c.CreateMultipartUpload(ctx, key)
	if err := c.AbortMultipartUpload(ctx, key, id2); err != nil {
		t.Fatal(err)
	}
	if err := c.AbortMultipartUpload(ctx, key, id2); err != nil && !strings.Contains(err.Error(), "NoSuchUpload") {
		t.Fatalf("second abort: %v", err)
	}
}
