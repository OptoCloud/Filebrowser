package r2

import (
	"net/url"
	"strings"
	"testing"
	"time"
)

// The example from the AWS documentation ("Authenticating Requests: Using
// Query Parameters"): a presigned GET of examplebucket/test.txt.
func TestPresignMatchesAWSExample(t *testing.T) {
	c := Credentials{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", Region: "us-east-1"}
	u, _ := url.Parse("https://examplebucket.s3.amazonaws.com/test.txt")
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	got := c.Presign("GET", u, nil, 24*time.Hour, at)
	const want = "X-Amz-Signature=aeeed9bbccd4d02ee5c0109b86d86835f995330da4c265957d157751f604d404"
	if !strings.HasSuffix(got, want) {
		t.Fatalf("presigned URL = %s\nwant suffix %s", got, want)
	}
	for _, part := range []string{"X-Amz-Algorithm=AWS4-HMAC-SHA256", "X-Amz-Credential=AKIAIOSFODNN7EXAMPLE%2F20130524%2Fus-east-1%2Fs3%2Faws4_request",
		"X-Amz-Date=20130524T000000Z", "X-Amz-Expires=86400", "X-Amz-SignedHeaders=host"} {
		if !strings.Contains(got, part) {
			t.Errorf("presigned URL lacks %s", part)
		}
	}
}

func TestURIEncode(t *testing.T) {
	if got := uriEncode("a b/c~d*é", true); got != "a%20b/c~d%2A%C3%A9" {
		t.Errorf("path: %s", got)
	}
	if got := uriEncode("a b/c", false); got != "a%20b%2Fc" {
		t.Errorf("query: %s", got)
	}
}
