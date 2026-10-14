package backupstore

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

// The signatures below were generated with botocore's S3SigV4Auth (an
// independent implementation of the same specification) using the fixed
// timestamp 2026-10-14T03:42:56Z, the AWS documentation example credentials and
// a virtual host / path style mix. They pin the signing code to an external
// reference instead of to itself.
const (
	goldenAccessKey = "AKIAIOSFODNN7EXAMPLE"
	goldenSecretKey = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	goldenDate      = "20261014T034256Z"
)

func GoldenTestCases() []struct {
	Name          string
	Method        string
	URL           string
	Body          string
	Region        string
	SessionToken  string
	PayloadHash   string
	Authorization string
} {
	body := strings.Repeat("servy backup payload", 10)
	part := strings.Repeat("x", 32)
	return []struct {
		Name          string
		Method        string
		URL           string
		Body          string
		Region        string
		SessionToken  string
		PayloadHash   string
		Authorization string
	}{
		{
			Name:          "put_path_style",
			Method:        "PUT",
			URL:           "http://minio.example.com:9000/servy-backups/panel-7f3a/srv-42/srv-42_2026-10-14_12-00-00.zip",
			Body:          body,
			Region:        "us-east-1",
			PayloadHash:   "e8618cf45bfa1b33e49f358944dd7cc7e1512e43d16c8fea8eff7584ea992bb5",
			Authorization: "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20261014/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=e1d48694d465b1f10d4536d37ffb4ee037701a096f9c5c85882ec21edc5d37d8",
		},
		{
			Name:          "get_virtual_host",
			Method:        "GET",
			URL:           "https://my-bucket.s3.eu-central-1.amazonaws.com/prefix/cluster-a/srv-9/archive.zip",
			Region:        "eu-central-1",
			PayloadHash:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			Authorization: "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20261014/eu-central-1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=3f7693f632a52d365eedfa86d4f4ea93a6234b67c2e2827bc75104991c42f9e9",
		},
		{
			Name:          "list_with_query",
			Method:        "GET",
			URL:           "https://s3.us-east-1.amazonaws.com/servy-backups?list-type=2&max-keys=1000&prefix=panel-7f3a%2Fsrv-42%2F&continuation-token=1ueGcxLPRx1Tr%2F2OaaJvWUJ4%3D",
			Region:        "us-east-1",
			PayloadHash:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			Authorization: "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20261014/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=014355f575986c0ac3da3f73d93874545ea145b714f2bfa3b674f23503835f4e",
		},
		{
			Name:          "delete_object",
			Method:        "DELETE",
			URL:           "https://s3.us-east-1.amazonaws.com/servy-backups/panel-7f3a/srv-42/old.zip",
			Region:        "us-east-1",
			PayloadHash:   "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			Authorization: "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20261014/us-east-1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date, Signature=5d473848a79bc3780d6da34ecd7f698fd9cb90498222d2048749244842e684e6",
		},
		{
			Name:          "put_part_with_token",
			Method:        "PUT",
			URL:           "https://s3.eu-west-1.amazonaws.com/servy-backups/big.zip?partNumber=2&uploadId=abc123DEF",
			Body:          part,
			Region:        "eu-west-1",
			SessionToken:  "SESSIONTOKENEXAMPLE",
			PayloadHash:   "c62e4615bd39e222572f3a1bf7c2132ea1e65b17ec805047bd6b2842c593493f",
			Authorization: "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20261014/eu-west-1/s3/aws4_request, SignedHeaders=host;x-amz-content-sha256;x-amz-date;x-amz-security-token, Signature=05baf3717e93d0aa4c9dd9f3168b19ba31fb455d93383bfaded637f18130b147",
		},
	}
}

func TestSigV4MatchesBotocore(t *testing.T) {
	for _, tc := range GoldenTestCases() {
		t.Run(tc.Name, func(t *testing.T) {
			var req *http.Request
			var err error
			if tc.Body == "" {
				req, err = http.NewRequest(tc.Method, tc.URL, nil)
			} else {
				req, err = http.NewRequest(tc.Method, tc.URL, strings.NewReader(tc.Body))
			}
			if err != nil {
				t.Fatalf("building request: %v", err)
			}
			req.Header.Set("x-amz-date", goldenDate)
			signer := sigv4{
				accessKey:    goldenAccessKey,
				secretKey:    goldenSecretKey,
				sessionToken: tc.SessionToken,
				region:       tc.Region,
				service:      "s3",
			}
			signer.sign(req, tc.PayloadHash)
			if got := req.Header.Get("Authorization"); got != tc.Authorization {
				t.Errorf("Authorization mismatch\n got: %s\nwant: %s", got, tc.Authorization)
			}
			if got := req.Header.Get("x-amz-date"); got != goldenDate {
				t.Errorf("x-amz-date = %q, want %q", got, goldenDate)
			}
			if got := req.Header.Get("x-amz-content-sha256"); got != tc.PayloadHash {
				t.Errorf("payload hash = %q, want %q", got, tc.PayloadHash)
			}
			if tc.SessionToken != "" && req.Header.Get("x-amz-security-token") != tc.SessionToken {
				t.Errorf("session token header missing")
			}
		})
	}
}

func TestSigV4DuplicateOfBotocoreForEmptyBody(t *testing.T) {
	emptyHash := sha256.Sum256(nil)
	if got := hex.EncodeToString(emptyHash[:]); got != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("empty payload hash changed: %s", got)
	}
	if got := payloadHashOf(nil); got != hex.EncodeToString(emptyHash[:]) {
		t.Errorf("payloadHashOf(nil) = %s", got)
	}
}

func TestAWSEncode(t *testing.T) {
	cases := map[string]string{
		"plain/key.zip":     "plain/key.zip",
		"prefix/a b/c+.zip": "prefix/a%20b/c%2B.zip",
		"slashes/are/kept":  "slashes/are/kept",
		"unreserved-_.~":    "unreserved-_.~",
		"unicode/архив.zip": "unicode/%D0%B0%D1%80%D1%85%D0%B8%D0%B2.zip",
		"token/1+2=3":       "token/1%2B2%3D3",
	}
	for in, want := range cases {
		if got := awsEncode(in, false); got != want {
			t.Errorf("awsEncode(%q) = %q, want %q", in, got, want)
		}
	}
	if got := awsEncode("a/b", true); got != "a%2Fb" {
		t.Errorf("awsEncode with slash encoding = %q", got)
	}
}

func TestEncodeQueryMatchesCanonicalForm(t *testing.T) {
	req, err := http.NewRequest("GET", "https://s3.us-east-1.amazonaws.com/bucket?list-type=2&prefix=panel-7f3a%2Fsrv-42%2F&continuation-token=1ueGcxLPRx1Tr%2F2OaaJvWUJ4%3D", nil)
	if err != nil {
		t.Fatal(err)
	}
	// The wire encoding has to be exactly the canonical encoding the signature
	// is computed over, otherwise S3 rejects the request.
	want := "continuation-token=1ueGcxLPRx1Tr%2F2OaaJvWUJ4%3D&list-type=2&prefix=panel-7f3a%2Fsrv-42%2F"
	if got := encodeQuery(req.URL.Query()); got != want {
		t.Errorf("encodeQuery = %q, want %q", got, want)
	}
	if got := canonicalQuery(req.URL); got != want {
		t.Errorf("canonicalQuery = %q, want %q", got, want)
	}
}
