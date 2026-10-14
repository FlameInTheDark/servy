package backupstore

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
)

// Tuning knobs of the S3 client. They are variables so tests can shrink them.
var (
	// partSize is the chunk size of a multipart upload. AWS requires at least
	// 5 MiB per part (except for the last one).
	partSize = int64(64 << 20)
	// multipartThreshold is the object size above which an upload is split.
	multipartThreshold = int64(128 << 20)
)

// s3Store talks to any S3 compatible service (AWS, MinIO, Cloudflare R2,
// Backblaze B2, DigitalOcean Spaces, Wasabi, …) over the REST API, signing
// every request with SigV4.
type s3Store struct {
	base      *url.URL
	region    string
	bucket    string
	prefix    string
	creds     sigv4
	pathStyle bool
	client    *http.Client
}

func newS3Store(settings map[string]string, prefix string) (*s3Store, error) {
	endpoint := strings.TrimSpace(settings["endpoint"])
	region := strings.TrimSpace(settings["region"])
	if region == "" {
		region = "us-east-1"
	}
	if endpoint == "" {
		endpoint = "https://s3." + region + ".amazonaws.com"
	}
	base, err := url.Parse(endpoint)
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("invalid endpoint %q", endpoint)
	}
	pathStyle := settings["path_style"] != "false"
	return &s3Store{
		base:      base,
		region:    region,
		bucket:    settings["bucket"],
		prefix:    prefix,
		pathStyle: pathStyle,
		creds: sigv4{
			accessKey:    settings["access_key"],
			secretKey:    settings["secret_key"],
			sessionToken: settings["session_token"],
			region:       region,
			service:      "s3",
		},
		client: newHTTPClient(settings["insecure"] == "true", 0),
	}, nil
}

func (s *s3Store) Kind() string { return KindS3 }

func (s *s3Store) Location() string {
	loc := "s3://" + s.bucket
	if s.prefix != "" {
		loc += "/" + s.prefix
	}
	return loc
}

// objectPath renders the request path: bucket (path style) + destination
// prefix + key. Bucket level operations (empty key) must not carry the prefix in
// the path — it belongs into the prefix query parameter there.
func (s *s3Store) objectPath(key string) string {
	segments := []string{}
	if base := strings.Trim(s.base.Path, "/"); base != "" {
		segments = append(segments, splitKey(base)...)
	}
	if s.pathStyle {
		segments = append(segments, s.bucket)
	}
	if key != "" {
		if s.prefix != "" {
			segments = append(segments, splitKey(s.prefix)...)
		}
		segments = append(segments, splitKey(key)...)
	}
	return "/" + strings.Join(segments, "/")
}

// urlFor builds the request URL of a key (or of the bucket when key is empty),
// with a path that is already encoded the way the signature covers it.
func (s *s3Store) urlFor(key string, query url.Values) *url.URL {
	raw := s.objectPath(key)
	u := &url.URL{
		Scheme:   s.base.Scheme,
		Host:     s.base.Host,
		Path:     raw,
		RawPath:  canonicalURI(&url.URL{Path: raw}),
		RawQuery: encodeQuery(query),
	}
	if !s.pathStyle {
		u.Host = s.bucket + "." + s.base.Host
	}
	return u
}

// newRequest prepares an unsigned request. Callers add their headers and sign.
func (s *s3Store) newRequest(ctx context.Context, method, key string, query url.Values, body io.Reader, size int64) (*http.Request, error) {
	u := s.urlFor(key, query)
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	return req, nil
}

// buildRequest prepares and signs a request with a streamed binary body.
func (s *s3Store) buildRequest(ctx context.Context, method, key string, query url.Values, body io.Reader, size int64, payloadHash string) (*http.Request, error) {
	req, err := s.newRequest(ctx, method, key, query, body, size)
	if err != nil {
		return nil, err
	}
	if body != nil && size != 0 {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	s.creds.sign(req, payloadHash)
	return req, nil
}

// do runs a request and turns S3 failures into readable errors.
func (s *s3Store) do(req *http.Request, op string) (*http.Response, []byte, error) {
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, body, nil
	}
	return resp, body, s3Error(op, resp, body)
}

// s3Error renders an S3 error document.
func s3Error(op string, resp *http.Response, body []byte) error {
	var doc struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	_ = xml.Unmarshal(body, &doc)
	detail := strings.TrimSpace(doc.Code)
	if doc.Message != "" {
		detail += " " + strings.TrimSpace(doc.Message)
	}
	detail = strings.TrimSpace(detail)
	if detail == "" {
		detail = strings.TrimSpace(string(body))
	}
	if detail == "" {
		detail = resp.Status
	}
	if resp.StatusCode == http.StatusNotFound && (doc.Code == "NoSuchKey" || doc.Code == "NotFound") {
		return fmt.Errorf("%w: %s", ErrNotExist, detail)
	}
	return fmt.Errorf("s3 %s failed (%d): %s", op, resp.StatusCode, truncate(detail, 300))
}

func (s *s3Store) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	if size < 0 {
		// S3 needs a Content-Length: spool unknown sizes to a temporary file.
		tmp, err := os.CreateTemp("", "servy-s3-*")
		if err != nil {
			return err
		}
		defer func() {
			tmp.Close()
			os.Remove(tmp.Name())
		}()
		written, err := copyContext(ctx, tmp, body)
		if err != nil {
			return err
		}
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			return err
		}
		return s.putStream(ctx, key, tmp, written)
	}
	return s.putStream(ctx, key, body, size)
}

// putStream uploads a body of known size, single shot or multipart.
func (s *s3Store) putStream(ctx context.Context, key string, body io.Reader, size int64) error {
	if size <= multipartThreshold {
		req, err := s.buildRequest(ctx, http.MethodPut, key, nil, body, size, unsignedPayload)
		if err != nil {
			return err
		}
		if _, _, err := s.do(req, "PUT "+key); err != nil {
			return err
		}
		return nil
	}
	return s.putMultipart(ctx, key, body, size)
}

// putMultipart splits a large object into parts.
func (s *s3Store) putMultipart(ctx context.Context, key string, body io.Reader, size int64) error {
	uploadID, err := s.initiateMultipart(ctx, key)
	if err != nil {
		return err
	}
	abort := func() {
		req, err := s.buildRequest(ctx, http.MethodDelete, key, url.Values{"uploadId": {uploadID}}, nil, -1, payloadHashOf(nil))
		if err == nil {
			_, _, _ = s.do(req, "AbortMultipartUpload")
		}
	}
	parts := []completedPart{}
	remaining := size
	for number := 1; remaining > 0; number++ {
		chunk := partSize
		if remaining < chunk {
			chunk = remaining
		}
		req, err := s.buildRequest(ctx, http.MethodPut, key,
			url.Values{"partNumber": {strconv.Itoa(number)}, "uploadId": {uploadID}},
			io.LimitReader(body, chunk), chunk, unsignedPayload)
		if err != nil {
			abort()
			return err
		}
		resp, _, err := s.do(req, fmt.Sprintf("UploadPart %d", number))
		if err != nil {
			abort()
			return err
		}
		parts = append(parts, completedPart{Number: number, ETag: resp.Header.Get("ETag")})
		remaining -= chunk
	}
	var payload bytes.Buffer
	payload.WriteString(`<?xml version="1.0" encoding="UTF-8"?><CompleteMultipartUpload xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	for _, part := range parts {
		fmt.Fprintf(&payload, "<Part><PartNumber>%d</PartNumber><ETag>%s</ETag></Part>", part.Number, part.ETag)
	}
	payload.WriteString(`</CompleteMultipartUpload>`)
	raw := payload.Bytes()
	req, err := s.newRequest(ctx, http.MethodPost, key, url.Values{"uploadId": {uploadID}}, bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		abort()
		return err
	}
	req.Header.Set("Content-Type", "application/xml")
	s.creds.sign(req, payloadHashOf(raw))
	if _, _, err := s.do(req, "CompleteMultipartUpload"); err != nil {
		abort()
		return err
	}
	return nil
}

type completedPart struct {
	Number int
	ETag   string
}

// initiateMultipart starts a multipart upload and returns its id.
func (s *s3Store) initiateMultipart(ctx context.Context, key string) (string, error) {
	req, err := s.buildRequest(ctx, http.MethodPost, key, url.Values{"uploads": {""}}, nil, -1, payloadHashOf(nil))
	if err != nil {
		return "", err
	}
	resp, body, err := s.do(req, "InitiateMultipartUpload")
	if err != nil {
		return "", err
	}
	_ = resp
	var doc struct {
		UploadID string `xml:"UploadId"`
	}
	if err := xml.Unmarshal(body, &doc); err != nil || doc.UploadID == "" {
		return "", fmt.Errorf("s3 multipart init returned no upload id (%s)", truncate(string(body), 200))
	}
	return doc.UploadID, nil
}

func (s *s3Store) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	req, err := s.buildRequest(ctx, http.MethodGet, key, nil, nil, -1, payloadHashOf(nil))
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusNotFound {
		resp.Body.Close()
		return nil, ErrNotExist
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		return nil, s3Error("GET "+key, resp, body)
	}
	return resp.Body, nil
}

func (s *s3Store) Delete(ctx context.Context, key string) error {
	req, err := s.buildRequest(ctx, http.MethodDelete, key, nil, nil, -1, payloadHashOf(nil))
	if err != nil {
		return err
	}
	resp, body, err := s.do(req, "DELETE "+key)
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return nil
		}
		return err
	}
	_ = body
	return nil
}

func (s *s3Store) List(ctx context.Context, prefix string) ([]Object, error) {
	out := []Object{}
	token := ""
	for {
		query := url.Values{"list-type": {"2"}, "max-keys": {"1000"}}
		if s.prefix != "" {
			full := s.prefix
			if clean := cleanPrefix(prefix); clean != "" {
				full = s.prefix + "/" + clean
			}
			query.Set("prefix", full)
		} else if clean := cleanPrefix(prefix); clean != "" {
			query.Set("prefix", clean)
		}
		if token != "" {
			query.Set("continuation-token", token)
		}
		req, err := s.buildRequest(ctx, http.MethodGet, "", query, nil, -1, payloadHashOf(nil))
		if err != nil {
			return nil, err
		}
		_, body, err := s.do(req, "ListObjectsV2")
		if err != nil {
			return nil, err
		}
		var doc struct {
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
			Contents              []struct {
				Key          string `xml:"Key"`
				Size         int64  `xml:"Size"`
				LastModified string `xml:"LastModified"`
			} `xml:"Contents"`
		}
		if err := xml.Unmarshal(body, &doc); err != nil {
			return nil, fmt.Errorf("s3 listing is not readable: %w", err)
		}
		for _, item := range doc.Contents {
			key, ok := relKey(item.Key, s.prefix)
			if !ok {
				continue
			}
			out = append(out, Object{Key: key, Size: item.Size, Modified: modTime(item.LastModified)})
		}
		if !doc.IsTruncated || doc.NextContinuationToken == "" {
			break
		}
		token = doc.NextContinuationToken
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (s *s3Store) Ping(ctx context.Context) error {
	query := url.Values{"list-type": {"2"}, "max-keys": {"1"}}
	req, err := s.buildRequest(ctx, http.MethodGet, "", query, nil, -1, payloadHashOf(nil))
	if err != nil {
		return err
	}
	resp, body, err := s.do(req, "Ping")
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusNotFound {
			return fmt.Errorf("bucket %q does not exist (%s)", s.bucket, truncate(string(body), 200))
		}
		return err
	}
	return nil
}

// truncate shortens a message for logs and API errors.
func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "…"
}
