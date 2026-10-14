package backupstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

/* ----------------------------- fake S3 server ------------------------------ */

// fakeS3 is a small S3-compatible object store. It verifies every request's
// SigV4 signature (recomputing it from the raw request, independently of the
// client's signing code) and implements the operations the backend uses,
// including multipart uploads. That makes it a protocol check, not a stub: a
// wrong canonical request, missing header or unencoded key fails the test.
type fakeS3 struct {
	mu       sync.Mutex
	objects  map[string][]byte
	modified map[string]time.Time
	uploads  map[string]map[int][]byte
	nextID   int
	requests []string

	region    string
	access    string
	secret    string
	requestsT int
}

func newFakeS3(access, secret, region string) *fakeS3 {
	return &fakeS3{
		objects:  map[string][]byte{},
		modified: map[string]time.Time{},
		uploads:  map[string]map[int][]byte{},
		region:   region,
		access:   access,
		secret:   secret,
	}
}

func (f *fakeS3) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if err := f.verify(r, body); err != nil {
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, fmt.Sprintf(`<Error><Code>SignatureDoesNotMatch</Code><Message>%s</Message></Error>`, err.Error()))
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
		f.mu.Unlock()
		f.serve(w, r, body)
	}
}

// verify recomputes the SigV4 signature of an incoming request.
func (f *fakeS3) verify(r *http.Request, body []byte) error {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		return fmt.Errorf("missing SigV4 authorization header")
	}
	fields := map[string]string{}
	for _, part := range strings.Split(strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 "), ",") {
		part = strings.TrimSpace(part)
		if i := strings.Index(part, "="); i > 0 {
			fields[part[:i]] = part[i+1:]
		}
	}
	credential := fields["Credential"]
	signedHeaders := fields["SignedHeaders"]
	signature := fields["Signature"]
	if credential == "" || signedHeaders == "" || signature == "" {
		return fmt.Errorf("incomplete authorization header: %s", auth)
	}
	credParts := strings.Split(credential, "/")
	if len(credParts) != 5 || credParts[4] != "aws4_request" {
		return fmt.Errorf("bad credential scope %q", credential)
	}
	accessKey, dateStamp, region, service := credParts[0], credParts[1], credParts[2], credParts[3]
	if accessKey != f.access || region != f.region || service != "s3" {
		return fmt.Errorf("unexpected credential scope %q", credential)
	}
	payloadHash := r.Header.Get("x-amz-content-sha256")
	if payloadHash != "" && payloadHash != unsignedPayload {
		sum := sha256.Sum256(body)
		if hex.EncodeToString(sum[:]) != payloadHash {
			return fmt.Errorf("payload hash mismatch")
		}
	}

	names := strings.Split(signedHeaders, ";")
	canonicalHeaders := strings.Builder{}
	for _, name := range names {
		value := ""
		if name == "host" {
			value = r.Host
		} else {
			value = r.Header.Get(name)
		}
		if value == "" {
			return fmt.Errorf("signed header %q missing", name)
		}
		canonicalHeaders.WriteString(name + ":" + strings.TrimSpace(value) + "\n")
	}
	query := r.URL.Query()
	keys := make([]string, 0, len(query))
	for k := range query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := []string{}
	for _, k := range keys {
		values := append([]string(nil), query[k]...)
		sort.Strings(values)
		for _, v := range values {
			parts = append(parts, awsEncode(k, true)+"="+awsEncode(v, true))
		}
	}
	canonicalRequest := strings.Join([]string{
		r.Method,
		r.URL.EscapedPath(),
		strings.Join(parts, "&"),
		canonicalHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	sum := sha256.Sum256([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		r.Header.Get("x-amz-date"),
		strings.Join([]string{dateStamp, region, service, "aws4_request"}, "/"),
		hex.EncodeToString(sum[:]),
	}, "\n")
	expected := hex.EncodeToString(signV4(f.secret, dateStamp, region, service, stringToSign))
	if expected != signature {
		return fmt.Errorf("signature mismatch: got %s want %s", signature, expected)
	}
	return nil
}

// signV4 derives the signing key and signs, written out explicitly here so the
// test does not share code with the implementation under test beyond the HMAC
// primitives.
func signV4(secret, dateStamp, region, service, stringToSign string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), dateStamp)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	kSigning := hmacSHA256(kService, "aws4_request")
	return hmacSHA256(kSigning, stringToSign)
}

// bucketOf strips the bucket from the request path (path and virtual host style).
func (f *fakeS3) objectKey(r *http.Request) string {
	path := strings.TrimPrefix(r.URL.EscapedPath(), "/")
	if !strings.Contains(r.Host, "s3.") && !strings.Contains(r.Host, "amazonaws.com") {
		// path style: /bucket/key
		slash := strings.Index(path, "/")
		if slash < 0 {
			return ""
		}
		// the bucket is part of the path; tests use a single bucket name
		return strings.TrimPrefix(path, path[:slash+1])
	}
	return path
}

func (f *fakeS3) serve(w http.ResponseWriter, r *http.Request, body []byte) {
	key := strings.TrimPrefix(r.URL.EscapedPath(), "/")
	// path style requests carry the bucket as first segment
	if strings.HasPrefix(r.URL.Path, "/servy-backups") {
		key = strings.TrimPrefix(strings.TrimPrefix(key, "servy-backups"), "/")
	}
	unescaped, err := url.PathUnescape(key)
	if err == nil {
		key = unescaped
	}
	query := r.URL.Query()

	switch {
	case r.Method == http.MethodPost && query.Has("uploads"):
		f.mu.Lock()
		f.nextID++
		id := fmt.Sprintf("upload-%d", f.nextID)
		f.uploads[id] = map[int][]byte{}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, "<InitiateMultipartUploadResult><Bucket>servy-backups</Bucket><Key>%s</Key><UploadId>%s</UploadId></InitiateMultipartUploadResult>", key, id)

	case r.Method == http.MethodPut && query.Has("partNumber"):
		uploadID := query.Get("uploadId")
		number, _ := strconv.Atoi(query.Get("partNumber"))
		f.mu.Lock()
		parts, ok := f.uploads[uploadID]
		if ok {
			parts[number] = body
		}
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("ETag", fmt.Sprintf("\"part-%d\"", number))
		w.WriteHeader(http.StatusOK)

	case r.Method == http.MethodPost && query.Has("uploadId"):
		uploadID := query.Get("uploadId")
		f.mu.Lock()
		parts, ok := f.uploads[uploadID]
		delete(f.uploads, uploadID)
		var assembled []byte
		numbers := make([]int, 0, len(parts))
		for n := range parts {
			numbers = append(numbers, n)
		}
		sort.Ints(numbers)
		for _, n := range numbers {
			assembled = append(assembled, parts[n]...)
		}
		if ok {
			f.objects[key] = assembled
			f.modified[key] = time.Now().UTC()
		}
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, "<CompleteMultipartUploadResult><Key>%s</Key></CompleteMultipartUploadResult>", key)

	case r.Method == http.MethodDelete && query.Has("uploadId"):
		f.mu.Lock()
		delete(f.uploads, query.Get("uploadId"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)

	case r.Method == http.MethodPut:
		f.mu.Lock()
		f.objects[key] = body
		f.modified[key] = time.Now().UTC()
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)

	case r.Method == http.MethodGet && query.Get("list-type") == "2":
		prefix := query.Get("prefix")
		f.mu.Lock()
		keys := make([]string, 0, len(f.objects))
		for k := range f.objects {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		type entry struct {
			Key          string `xml:"Key"`
			Size         int64  `xml:"Size"`
			LastModified string `xml:"LastModified"`
		}
		out := struct {
			XMLName     xml.Name `xml:"ListBucketResult"`
			IsTruncated bool     `xml:"IsTruncated"`
			Contents    []entry  `xml:"Contents"`
		}{IsTruncated: false}
		for _, k := range keys {
			out.Contents = append(out.Contents, entry{Key: k, Size: int64(len(f.objects[k])), LastModified: f.modified[k].Format(time.RFC3339)})
		}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		xml.NewEncoder(w).Encode(out)

	case r.Method == http.MethodGet:
		f.mu.Lock()
		data, ok := f.objects[key]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, "<Error><Code>NoSuchKey</Code><Message>not found</Message></Error>")
			return
		}
		w.Write(data)

	case r.Method == http.MethodDelete:
		f.mu.Lock()
		delete(f.objects, key)
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)

	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (f *fakeS3) keys() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0, len(f.objects))
	for k := range f.objects {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (f *fakeS3) content(key string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return string(f.objects[key])
}

/* --------------------------------- tests ---------------------------------- */

func startFakeS3(t *testing.T) (*fakeS3, string) {
	t.Helper()
	f := newFakeS3("AKIAIOSFODNN7EXAMPLE", "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "eu-central-1")
	srv := httptest.NewServer(f.Handler())
	t.Cleanup(srv.Close)
	return f, srv.URL
}

func s3Destination(t *testing.T, endpoint, prefix string) Destination {
	t.Helper()
	return Destination{
		Kind:   KindS3,
		Prefix: prefix,
		Settings: map[string]string{
			"bucket":     "servy-backups",
			"endpoint":   endpoint,
			"region":     "eu-central-1",
			"access_key": "AKIAIOSFODNN7EXAMPLE",
			"secret_key": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
			"path_style": "true",
		},
	}
}

func TestS3StoreRoundTrip(t *testing.T) {
	fake, endpoint := startFakeS3(t)
	store, err := Open(s3Destination(t, endpoint, "servy/prod"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	payload := []byte(strings.Repeat("s3-archive-", 2000))
	key := Key("cluster-7f3a", "srv-42", "srv-42_2026-10-14.zip")
	if err := store.Put(ctx, key, bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := fake.keys(); len(got) != 1 || got[0] != "servy/prod/cluster-7f3a/srv-42/srv-42_2026-10-14.zip" {
		t.Fatalf("unexpected keys: %v", got)
	}
	if fake.content(fake.keys()[0]) != string(payload) {
		t.Fatal("stored content differs")
	}

	rc, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	back, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(back, payload) {
		t.Fatalf("downloaded %d bytes, want %d", len(back), len(payload))
	}

	objects, err := store.List(ctx, ClusterPrefix("cluster-7f3a"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objects) != 1 || objects[0].Key != key || objects[0].Size != int64(len(payload)) {
		t.Fatalf("unexpected listing: %+v", objects)
	}
	other, err := store.List(ctx, ClusterPrefix("cluster-other"))
	if err != nil {
		t.Fatalf("List other: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("cluster isolation broken: %+v", other)
	}
	if _, err := store.Get(ctx, Key("cluster-7f3a", "srv-42", "missing.zip")); err != ErrNotExist {
		t.Fatalf("expected ErrNotExist, got %v", err)
	}

	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(fake.keys()) != 0 {
		t.Fatalf("object not deleted: %v", fake.keys())
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}

func TestS3MultipartUpload(t *testing.T) {
	fake, endpoint := startFakeS3(t)
	store, err := Open(s3Destination(t, endpoint, "big"))
	if err != nil {
		t.Fatal(err)
	}
	// Shrink the multipart thresholds so the test stays fast.
	oldSize, oldThreshold := partSize, multipartThreshold
	partSize = 1 << 10
	multipartThreshold = 2 << 10
	t.Cleanup(func() { partSize, multipartThreshold = oldSize, oldThreshold })

	payload := bytes.Repeat([]byte("multipart-payload-"), 500) // ~9 KiB -> 9 parts
	key := Key("c1", "s1", "large.zip")
	if err := store.Put(context.Background(), key, bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	keys := fake.keys()
	if len(keys) != 1 || keys[0] != "big/c1/s1/large.zip" {
		t.Fatalf("unexpected keys: %v", keys)
	}
	if got := fake.content(keys[0]); got != string(payload) {
		t.Fatalf("assembled object differs: %d vs %d bytes", len(got), len(payload))
	}
	// every part must have carried a correct payload hash (verified by the fake)
	if len(fake.requests) < 5 {
		t.Fatalf("expected several requests, got %v", fake.requests)
	}
}

func TestS3UploadWithoutKnownSize(t *testing.T) {
	fake, endpoint := startFakeS3(t)
	store, err := Open(s3Destination(t, endpoint, ""))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("streamed without a known length")
	// A reader that is not seekable and a negative size must still upload
	// correctly (the backend spools it to a temp file).
	if err := store.Put(context.Background(), Key("c", "s", "stream.zip"), struct{ io.Reader }{bytes.NewReader(payload)}, -1); err != nil {
		t.Fatalf("Put: %v", err)
	}
	keys := fake.keys()
	if len(keys) != 1 || fake.content(keys[0]) != string(payload) {
		t.Fatalf("unexpected content: %v", keys)
	}
}

func TestS3VirtualHostAndCustomEndpoint(t *testing.T) {
	// A virtual host style destination resolves the bucket as a sub domain, so
	// pointing it at a bare IP has to fail with a lookup error instead of
	// silently sending a malformed request.
	fake, endpoint := startFakeS3(t)
	dest := s3Destination(t, endpoint, "")
	dest.Settings["path_style"] = "false"
	store, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Ping(context.Background()); err == nil {
		t.Fatal("expected the request to fail")
	}
	if len(fake.requests) != 0 {
		t.Fatalf("the fake should not have processed a request: %v", fake.requests)
	}
}

func TestS3RequestShaping(t *testing.T) {
	// Path style: the bucket stays in the path, the key is escaped.
	pathStore, err := newS3Store(map[string]string{
		"bucket": "servy-backups", "endpoint": "http://minio.example.com:9000",
		"region": "eu-central-1", "access_key": "AK", "secret_key": "SK", "path_style": "true",
	}, "prod/cluster")
	if err != nil {
		t.Fatal(err)
	}
	// the key is relative to the destination prefix
	req, err := pathStore.buildRequest(context.Background(), "PUT", "c1/s1/archive with space.zip", nil, bytes.NewReader([]byte("x")), 1, payloadHashOf([]byte("x")))
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.Host != "minio.example.com:9000" {
		t.Errorf("host = %q", req.URL.Host)
	}
	if got := req.URL.EscapedPath(); got != "/servy-backups/prod/cluster/c1/s1/archive%20with%20space.zip" {
		t.Errorf("path = %q", got)
	}
	if auth := req.Header.Get("Authorization"); !strings.Contains(auth, "SignedHeaders=content-type;host;x-amz-content-sha256;x-amz-date") {
		t.Errorf("authorization header not signed: %q", auth)
	}
	if req.ContentLength != 1 {
		t.Errorf("content length = %d, want the known size (S3 rejects chunked bodies)", req.ContentLength)
	}

	// Virtual host style: the bucket becomes the host prefix, the path is the key.
	virtualStore, err := newS3Store(map[string]string{
		"bucket": "servy-backups", "endpoint": "https://s3.eu-central-1.amazonaws.com",
		"region": "eu-central-1", "access_key": "AK", "secret_key": "SK", "path_style": "false",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	req, err = virtualStore.buildRequest(context.Background(), "GET", "c1/s1/archive.zip", url.Values{"list-type": {"2"}, "prefix": {"c1/s1/"}}, nil, -1, payloadHashOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.Host != "servy-backups.s3.eu-central-1.amazonaws.com" {
		t.Errorf("host = %q", req.URL.Host)
	}
	if req.URL.Path != "/c1/s1/archive.zip" {
		t.Errorf("path = %q", req.URL.Path)
	}
	if got := req.URL.Query().Get("prefix"); got != "c1/s1/" {
		t.Errorf("prefix = %q", got)
	}
	// The wire query must be the canonical form the signature covers.
	if !strings.Contains(req.URL.RawQuery, "prefix=c1%2Fs1%2F") {
		t.Errorf("raw query not canonically encoded: %q", req.URL.RawQuery)
	}

	// AWS default endpoint for a bare configuration.
	def, err := newS3Store(map[string]string{
		"bucket": "b", "access_key": "AK", "secret_key": "SK",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	req, err = def.buildRequest(context.Background(), "GET", "k", nil, nil, -1, payloadHashOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.Host != "b.s3.us-east-1.amazonaws.com" && req.URL.Host != "s3.us-east-1.amazonaws.com" {
		t.Errorf("default endpoint host = %q", req.URL.Host)
	}
	if def.Location() != "s3://b" {
		t.Errorf("location = %q", def.Location())
	}
}

func TestS3WrongCredentialsAreRejected(t *testing.T) {
	_, endpoint := startFakeS3(t)
	dest := s3Destination(t, endpoint, "")
	dest.Settings["secret_key"] = "wrong-secret"
	store, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	err = store.Ping(context.Background())
	if err == nil {
		t.Fatal("expected a signature error")
	}
	if !strings.Contains(err.Error(), "SignatureDoesNotMatch") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestS3BucketPrefixIsolation(t *testing.T) {
	fake, endpoint := startFakeS3(t)
	first, err := Open(s3Destination(t, endpoint, "tenant-a"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(s3Destination(t, endpoint, "tenant-b"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := Key("c1", "s1", "a.zip")
	if err := first.Put(ctx, key, strings.NewReader("a"), 1); err != nil {
		t.Fatal(err)
	}
	if err := second.Put(ctx, key, strings.NewReader("b"), 1); err != nil {
		t.Fatal(err)
	}
	if got := fake.keys(); len(got) != 2 {
		t.Fatalf("unexpected keys: %v", got)
	}
	list, err := first.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Key != key {
		t.Fatalf("prefix isolation broken: %+v", list)
	}
}

// TestS3ExternalEndpoint runs the round trip against a real S3/MinIO endpoint
// when SERVY_TEST_S3_ENDPOINT is configured.
func TestS3ExternalEndpoint(t *testing.T) {
	endpoint := getenvAny("SERVY_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("SERVY_TEST_S3_ENDPOINT not set")
	}
	dest := Destination{
		Kind:   KindS3,
		Prefix: "servy-test",
		Settings: map[string]string{
			"bucket":        getenvAny("SERVY_TEST_S3_BUCKET", "servy-test"),
			"endpoint":      endpoint,
			"region":        getenvAny("SERVY_TEST_S3_REGION", "us-east-1"),
			"access_key":    getenvAny("SERVY_TEST_S3_ACCESS_KEY"),
			"secret_key":    getenvAny("SERVY_TEST_S3_SECRET_KEY"),
			"path_style":    getenvAny("SERVY_TEST_S3_PATH_STYLE", "true"),
			"session_token": getenvAny("SERVY_TEST_S3_SESSION_TOKEN"),
		},
	}
	store, err := Open(dest)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	key := Key("cluster-external", "srv-x", "roundtrip.zip")
	payload := []byte("external s3 round trip")
	if err := store.Put(ctx, key, bytes.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("Put: %v", err)
	}
	rc, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	back, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(back, payload) {
		t.Fatalf("content mismatch: %q", back)
	}
	objects, err := store.List(ctx, ClusterPrefix("cluster-external"))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objects) == 0 {
		t.Fatal("listing empty")
	}
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_ = strconv.Itoa(0)
}
