package r2

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client does the object operations of one bucket.
type Client struct {
	creds    Credentials
	endpoint *url.URL // e.g. https://<account>.r2.cloudflarestorage.com
	bucket   string
	http     *http.Client
	now      func() time.Time
}

// ErrNotFound reports an object or upload that does not exist.
var ErrNotFound = errors.New("r2: not found")

func New(endpoint, bucket string, creds Credentials) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(endpoint, "/"))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || bucket == "" {
		return nil, errors.New("r2: endpoint must be an http(s) URL and bucket a name")
	}
	return &Client{creds: creds, endpoint: u, bucket: bucket, now: time.Now,
		// No overall timeout: an object can take long to download. Requests
		// carry contexts.
		http: &http.Client{Transport: &http.Transport{
			ResponseHeaderTimeout: time.Minute, TLSHandshakeTimeout: 10 * time.Second,
			MaxIdleConns: 10, IdleConnTimeout: time.Minute, Proxy: http.ProxyFromEnvironment,
		}}}, nil
}

// Origin is the scheme and host uploads are sent to, for the pages'
// Content-Security-Policy.
func (c *Client) Origin() string { return c.endpoint.Scheme + "://" + c.endpoint.Host }

// objectURL addresses key path-style: endpoint/bucket/key.
func (c *Client) objectURL(key string, q url.Values) *url.URL {
	u := *c.endpoint
	u.Path = strings.TrimRight(u.Path, "/") + "/" + c.bucket + "/" + key
	u.RawQuery = q.Encode()
	return &u
}

// do sends a signed request and returns the response if its status is 2xx.
func (c *Client) do(ctx context.Context, method, key string, q url.Values, body []byte) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.objectURL(key, q).String(), rd)
	if err != nil {
		return nil, err
	}
	c.creds.Sign(req, c.now())
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode >= 200 && res.StatusCode <= 299 {
		return res, nil
	}
	defer res.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(res.Body, 2<<10))
	if res.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	var e struct {
		Code, Message string
	}
	if xml.Unmarshal(msg, &e) == nil && e.Code != "" {
		return nil, fmt.Errorf("r2: %s %s: %s: %s", method, res.Status, e.Code, e.Message)
	}
	return nil, fmt.Errorf("r2: %s %s", method, res.Status)
}

func (c *Client) xml(ctx context.Context, method, key string, q url.Values, body []byte, out any) error {
	res, err := c.do(ctx, method, key, q, body)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if out == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
		return nil
	}
	return xml.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out)
}

// CreateMultipartUpload starts an upload of key in parts and returns its id.
func (c *Client) CreateMultipartUpload(ctx context.Context, key string) (string, error) {
	var out struct {
		UploadID string `xml:"UploadId"`
	}
	if err := c.xml(ctx, http.MethodPost, key, url.Values{"uploads": {""}}, nil, &out); err != nil {
		return "", err
	}
	if out.UploadID == "" {
		return "", errors.New("r2: no upload id in the answer")
	}
	return out.UploadID, nil
}

// PresignPart returns an address to which part number n (from 1) of exactly
// size bytes can be PUT, by anyone, until ttl has passed. The size is part
// of what is signed: a body of another length is refused by the store.
func (c *Client) PresignPart(key, uploadID string, n int, size int64, ttl time.Duration) string {
	u := c.objectURL(key, url.Values{"partNumber": {strconv.Itoa(n)}, "uploadId": {uploadID}})
	return c.creds.Presign(http.MethodPut, u, map[string]string{"content-length": strconv.FormatInt(size, 10)}, ttl, c.now())
}

// Part is one uploaded part of a multipart upload.
type Part struct {
	Number int    `xml:"PartNumber" json:"n"`
	ETag   string `xml:"ETag" json:"etag"`
	Size   int64  `xml:"Size" json:"size"`
}

// ListParts returns the parts uploaded so far, in order.
func (c *Client) ListParts(ctx context.Context, key, uploadID string) ([]Part, error) {
	var parts []Part
	marker := ""
	for {
		q := url.Values{"uploadId": {uploadID}, "max-parts": {"1000"}}
		if marker != "" {
			q.Set("part-number-marker", marker)
		}
		var out struct {
			Parts       []Part `xml:"Part"`
			IsTruncated bool
			NextMarker  string `xml:"NextPartNumberMarker"`
		}
		if err := c.xml(ctx, http.MethodGet, key, q, nil, &out); err != nil {
			return nil, err
		}
		for i := range out.Parts {
			out.Parts[i].ETag = strings.Trim(out.Parts[i].ETag, `"`)
		}
		parts = append(parts, out.Parts...)
		if !out.IsTruncated || out.NextMarker == "" || out.NextMarker == marker || len(parts) > 10000 {
			return parts, nil
		}
		marker = out.NextMarker
	}
}

// CompleteMultipartUpload makes the object out of parts.
func (c *Client) CompleteMultipartUpload(ctx context.Context, key, uploadID string, parts []Part) error {
	type part struct {
		PartNumber int
		ETag       string
	}
	body := struct {
		XMLName xml.Name `xml:"CompleteMultipartUpload"`
		Parts   []part   `xml:"Part"`
	}{}
	for _, p := range parts {
		body.Parts = append(body.Parts, part{p.Number, `"` + p.ETag + `"`})
	}
	raw, err := xml.Marshal(body)
	if err != nil {
		return err
	}
	// A 200 can still carry an error document.
	var out struct {
		XMLName xml.Name
		Code    string
		Message string
	}
	if err := c.xml(ctx, http.MethodPost, key, url.Values{"uploadId": {uploadID}}, raw, &out); err != nil {
		return err
	}
	if out.XMLName.Local == "Error" {
		return fmt.Errorf("r2: complete: %s: %s", out.Code, out.Message)
	}
	return nil
}

// AbortMultipartUpload discards an unfinished upload and its parts. One
// that is already gone is not an error.
func (c *Client) AbortMultipartUpload(ctx context.Context, key, uploadID string) error {
	err := c.xml(ctx, http.MethodDelete, key, url.Values{"uploadId": {uploadID}}, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Head returns the size of an object.
func (c *Client) Head(ctx context.Context, key string) (int64, error) {
	res, err := c.do(ctx, http.MethodHead, key, nil, nil)
	if err != nil {
		return 0, err
	}
	res.Body.Close()
	return res.ContentLength, nil
}

// Get opens an object for reading; the caller closes it.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	res, err := c.do(ctx, http.MethodGet, key, nil, nil)
	if err != nil {
		return nil, 0, err
	}
	return res.Body, res.ContentLength, nil
}

// Delete removes an object. One that is already gone is not an error.
func (c *Client) Delete(ctx context.Context, key string) error {
	err := c.xml(ctx, http.MethodDelete, key, nil, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}
