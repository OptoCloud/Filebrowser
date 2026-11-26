// Package r2 talks to Cloudflare R2 (or any S3-compatible store) with the
// handful of S3 operations the upload buffer needs, signed with AWS
// Signature Version 4. It is written out here rather than taken from an SDK:
// the whole of it is one signature scheme and seven requests.
package r2

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	algorithm = "AWS4-HMAC-SHA256"
	// unsignedPayload leaves the body out of the signature (it is sent over
	// TLS, and for a presigned upload it is not known when signing).
	unsignedPayload = "UNSIGNED-PAYLOAD"
	amzTime         = "20060102T150405Z"
)

// Credentials are an S3 access key pair. Region is "auto" for R2.
type Credentials struct {
	AccessKeyID, SecretAccessKey, Region string
}

func (c Credentials) region() string {
	if c.Region == "" {
		return "auto"
	}
	return c.Region
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func hexSHA256(data string) string {
	sum := sha256.Sum256([]byte(data))
	return hex.EncodeToString(sum[:])
}

// uriEncode is SigV4's encoding: every byte but A-Z a-z 0-9 - _ . ~ becomes
// %XX in upper case; a slash is kept in a path and encoded in a query.
func uriEncode(s string, keepSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9', c == '-', c == '_', c == '.', c == '~',
			c == '/' && keepSlash:
			b.WriteByte(c)
		default:
			const hexUpper = "0123456789ABCDEF"
			b.WriteByte('%')
			b.WriteByte(hexUpper[c>>4])
			b.WriteByte(hexUpper[c&15])
		}
	}
	return b.String()
}

// canonicalQuery orders and encodes query parameters as SigV4 wants them.
func canonicalQuery(q url.Values) string {
	parts := make([]string, 0, len(q))
	for k, vs := range q {
		for _, v := range vs {
			parts = append(parts, uriEncode(k, false)+"="+uriEncode(v, false))
		}
	}
	sort.Strings(parts)
	return strings.Join(parts, "&")
}

// signature computes the SigV4 signature of a request described by its
// canonical parts. headers are lower-case names to values; all are signed.
func (c Credentials) signature(method, path, query string, headers map[string]string, payloadHash string, at time.Time) (sig, signedHeaders, scope string) {
	names := make([]string, 0, len(headers))
	for k := range headers {
		names = append(names, k)
	}
	sort.Strings(names)
	var canon strings.Builder
	for _, k := range names {
		canon.WriteString(k + ":" + strings.TrimSpace(headers[k]) + "\n")
	}
	signedHeaders = strings.Join(names, ";")
	date := at.UTC().Format("20060102")
	scope = date + "/" + c.region() + "/s3/aws4_request"
	request := strings.Join([]string{method, path, query, canon.String(), signedHeaders, payloadHash}, "\n")
	toSign := strings.Join([]string{algorithm, at.UTC().Format(amzTime), scope, hexSHA256(request)}, "\n")
	key := hmacSHA256([]byte("AWS4"+c.SecretAccessKey), date)
	for _, part := range []string{c.region(), "s3", "aws4_request"} {
		key = hmacSHA256(key, part)
	}
	return hex.EncodeToString(hmacSHA256(key, toSign)), signedHeaders, scope
}

// Presign returns u with query-string authentication added, valid for ttl:
// whoever has the address can make that one request, with exactly the
// headers given (a content-length among them fixes the size of an upload).
func (c Credentials) Presign(method string, u *url.URL, headers map[string]string, ttl time.Duration, at time.Time) string {
	signed := map[string]string{"host": u.Host}
	for k, v := range headers {
		signed[strings.ToLower(k)] = v
	}
	names := make([]string, 0, len(signed))
	for k := range signed {
		names = append(names, k)
	}
	sort.Strings(names)
	q := u.Query()
	date := at.UTC().Format("20060102")
	q.Set("X-Amz-Algorithm", algorithm)
	q.Set("X-Amz-Credential", c.AccessKeyID+"/"+date+"/"+c.region()+"/s3/aws4_request")
	q.Set("X-Amz-Date", at.UTC().Format(amzTime))
	q.Set("X-Amz-Expires", strconv.FormatInt(int64(ttl/time.Second), 10))
	q.Set("X-Amz-SignedHeaders", strings.Join(names, ";"))
	query := canonicalQuery(q)
	path := uriEncode(u.Path, true)
	sig, _, _ := c.signature(method, path, query, signed, unsignedPayload, at)
	return u.Scheme + "://" + u.Host + path + "?" + query + "&X-Amz-Signature=" + sig
}

// Sign adds header authentication to a request the server itself is about
// to send. The body is not part of the signature.
func (c Credentials) Sign(req *http.Request, at time.Time) {
	req.Header.Set("X-Amz-Date", at.UTC().Format(amzTime))
	req.Header.Set("X-Amz-Content-Sha256", unsignedPayload)
	headers := map[string]string{"host": req.URL.Host}
	for k, v := range req.Header {
		if lk := strings.ToLower(k); strings.HasPrefix(lk, "x-amz-") {
			headers[lk] = strings.Join(v, ",")
		}
	}
	path := uriEncode(req.URL.Path, true)
	// The request goes out with the path encoded the way it was signed.
	req.URL.RawPath = path
	sig, signedHeaders, scope := c.signature(req.Method, path, canonicalQuery(req.URL.Query()), headers, unsignedPayload, at)
	req.URL.RawQuery = canonicalQuery(req.URL.Query())
	req.Header.Set("Authorization", algorithm+" Credential="+c.AccessKeyID+"/"+scope+
		", SignedHeaders="+signedHeaders+", Signature="+sig)
}
