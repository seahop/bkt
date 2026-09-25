package middleware

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/gin-gonic/gin"
)

// verifyCapturedHeaderRequest verifies an SDK-signed (header auth) request
// the way the middleware does.
func verifyCapturedHeaderRequest(t *testing.T, sr *http.Request) error {
	t.Helper()
	a, err := parseAuthorizationHeader(sr.Header.Get("Authorization"))
	if err != nil {
		t.Fatalf("parse authorization: %v", err)
	}
	_, err = verifyHeaderSignature(sr, a, docSecretKey, time.Now())
	return err
}

func isUnsignedHeadersErr(err error) bool {
	var ue *unsignedHeadersError
	return errors.As(err, &ue)
}

// presignedServerRequest turns an SDK presign result into a server request,
// sending exactly the headers the presigner said must be sent.
func presignedServerRequest(method, rawURL string, signed http.Header) *http.Request {
	r := httptest.NewRequest(method, rawURL, strings.NewReader("payload"))
	for k, vs := range signed {
		if strings.EqualFold(k, "host") {
			continue
		}
		r.Header[http.CanonicalHeaderKey(k)] = append([]string(nil), vs...)
	}
	return r
}

func TestUnsignedAmzHeaders_PresignedPut(t *testing.T) {
	ps := s3.NewPresignClient(sdkClient(&captureClient{}))
	out, err := ps.PresignPutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String("bucket"), Key: aws.String("upload.bin"),
	}, func(o *s3.PresignOptions) { o.Expires = 15 * time.Minute })
	if err != nil {
		t.Fatal(err)
	}
	build := func() (*http.Request, url.Values) {
		r := presignedServerRequest(http.MethodPut, out.URL, out.SignedHeader)
		q, _ := url.ParseQuery(r.URL.RawQuery)
		return r, q
	}

	// Plain use of the link (what a browser does, incl. Content-Type).
	r, q := build()
	r.Header.Set("Content-Type", "application/octet-stream")
	if err := verifyPresignedSignature(r, q, docSecretKey); err != nil {
		t.Fatalf("SDK presigned PUT failed to verify: %v (%s)", err, out.URL)
	}

	// x-amz-content-sha256 may accompany a presigned request unsigned.
	r, q = build()
	r.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
	if err := verifyPresignedSignature(r, q, docSecretKey); err != nil {
		t.Fatalf("presigned PUT with unsigned x-amz-content-sha256 rejected: %v", err)
	}

	// The exfiltration attack: turn the upload link into a server-side copy.
	for _, h := range []string{"X-Amz-Copy-Source", "X-Amz-Tagging", "X-Amz-Meta-Evil", "X-Amz-Acl", "X-Amz-Storage-Class"} {
		r, q = build()
		r.Header.Set(h, "victim-bucket/secret.txt")
		err := verifyPresignedSignature(r, q, docSecretKey)
		if !isUnsignedHeadersErr(err) {
			t.Errorf("presigned PUT + unsigned %s: err = %v, want unsigned-headers error", h, err)
			continue
		}
		if !strings.Contains(err.Error(), strings.ToLower(h)) ||
			!strings.HasPrefix(err.Error(), "There were headers present in the request which were not signed") {
			t.Errorf("unexpected message %q", err.Error())
		}
	}
}

func TestUnsignedAmzHeaders_PresignedPutWithSignedMetadata(t *testing.T) {
	ps := s3.NewPresignClient(sdkClient(&captureClient{}))
	out, err := ps.PresignPutObject(context.Background(), &s3.PutObjectInput{
		Bucket:      aws.String("bucket"),
		Key:         aws.String("upload.bin"),
		ContentType: aws.String("text/plain"),
		Metadata:    map[string]string{"owner": "alice"},
		Tagging:     aws.String("a=b"),
	}, func(o *s3.PresignOptions) { o.Expires = 15 * time.Minute })
	if err != nil {
		t.Fatal(err)
	}
	r := presignedServerRequest(http.MethodPut, out.URL, out.SignedHeader)
	q, _ := url.ParseQuery(r.URL.RawQuery)
	if err := verifyPresignedSignature(r, q, docSecretKey); err != nil {
		t.Fatalf("presigned PUT with signed metadata failed: %v (url %s, headers %v)", err, out.URL, out.SignedHeader)
	}
}

func TestUnsignedAmzHeaders_SDKHeaderAuthOperations(t *testing.T) {
	ctx := context.Background()
	body := []byte("hello world")
	ops := map[string]func(c *s3.Client){
		"PutObject": func(c *s3.Client) {
			_, _ = c.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String("bucket"), Key: aws.String("k"), Body: bytes.NewReader(body)})
		},
		"PutObjectChecksumMetaTaggingACL": func(c *s3.Client) {
			_, _ = c.PutObject(ctx, &s3.PutObjectInput{
				Bucket: aws.String("bucket"), Key: aws.String("k"), Body: bytes.NewReader(body),
				ChecksumAlgorithm: types.ChecksumAlgorithmSha256,
				Metadata:          map[string]string{"a": "b"},
				Tagging:           aws.String("x=y"),
				ACL:               types.ObjectCannedACLPrivate,
				StorageClass:      types.StorageClassStandard,
				ContentType:       aws.String("text/plain"),
			})
		},
		"PutObjectCRC32": func(c *s3.Client) {
			_, _ = c.PutObject(ctx, &s3.PutObjectInput{
				Bucket: aws.String("bucket"), Key: aws.String("k"), Body: bytes.NewReader(body),
				ChecksumAlgorithm: types.ChecksumAlgorithmCrc32,
			})
		},
		"CreateMultipartUpload": func(c *s3.Client) {
			_, _ = c.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String("bucket"), Key: aws.String("k")})
		},
		"UploadPart": func(c *s3.Client) {
			_, _ = c.UploadPart(ctx, &s3.UploadPartInput{
				Bucket: aws.String("bucket"), Key: aws.String("k"), UploadId: aws.String("u1"),
				PartNumber: aws.Int32(1), Body: bytes.NewReader(body),
			})
		},
		"CompleteMultipartUpload": func(c *s3.Client) {
			_, _ = c.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
				Bucket: aws.String("bucket"), Key: aws.String("k"), UploadId: aws.String("u1"),
				MultipartUpload: &types.CompletedMultipartUpload{Parts: []types.CompletedPart{{PartNumber: aws.Int32(1), ETag: aws.String("\"e\"")}}},
			})
		},
		"CopyObject": func(c *s3.Client) {
			_, _ = c.CopyObject(ctx, &s3.CopyObjectInput{
				Bucket: aws.String("bucket"), Key: aws.String("dst"), CopySource: aws.String("src-bucket/src key.txt"),
				MetadataDirective: types.MetadataDirectiveReplace, Metadata: map[string]string{"m": "v"},
			})
		},
		"DeleteObjects": func(c *s3.Client) {
			_, _ = c.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: aws.String("bucket"),
				Delete: &types.Delete{Objects: []types.ObjectIdentifier{{Key: aws.String("a")}}},
			})
		},
	}
	for name, op := range ops {
		hc := &captureClient{}
		op(sdkClient(hc))
		if hc.req == nil {
			t.Fatalf("%s: no request captured", name)
		}
		// Only the headers matter for the signature; keep the signed length.
		sr := toServerRequest(t, hc.req, nil)
		sr.ContentLength = hc.req.ContentLength
		if err := verifyCapturedHeaderRequest(t, sr); err != nil {
			t.Errorf("%s: SDK-signed request rejected: %v (headers %v)", name, err, sr.Header)
		}
	}
}

func TestUnsignedAmzHeaders_HeaderAuthInjection(t *testing.T) {
	hc := &captureClient{}
	_, _ = sdkClient(hc).PutObject(context.Background(), &s3.PutObjectInput{
		Bucket: aws.String("bucket"), Key: aws.String("k"), Body: bytes.NewReader([]byte("x")),
	})
	sr := toServerRequest(t, hc.req, []byte("x"))
	sr.Header.Set("X-Amz-Copy-Source", "other/secret")
	if err := verifyCapturedHeaderRequest(t, sr); !isUnsignedHeadersErr(err) {
		t.Fatalf("header-auth request with injected x-amz-copy-source: %v", err)
	}
}

func TestCheckAmzHeadersSigned_Rules(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/b/k", nil)
	r.Header.Set("X-Amz-Date", "20240101T000000Z")
	r.Header.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")
	r.Header.Set("Content-Type", "text/plain") // non-x-amz headers need not be signed

	if err := checkAmzHeadersSigned(r, "host;x-amz-content-sha256;x-amz-date", false); err != nil {
		t.Errorf("all signed: %v", err)
	}
	err := checkAmzHeadersSigned(r, "host;x-amz-date", false)
	if !isUnsignedHeadersErr(err) || !strings.Contains(err.Error(), "x-amz-content-sha256") {
		t.Errorf("header auth: unsigned x-amz-content-sha256 must be rejected, got %v", err)
	}
	if err := checkAmzHeadersSigned(r, "host;x-amz-date", true); err != nil {
		t.Errorf("presigned: unsigned x-amz-content-sha256 must be allowed, got %v", err)
	}
	err = checkAmzHeadersSigned(r, "host;x-amz-content-sha256", false)
	if !isUnsignedHeadersErr(err) || !strings.Contains(err.Error(), "x-amz-date") {
		t.Errorf("unsigned x-amz-date must be rejected, got %v", err)
	}
}

func TestComputeHeaderSignature_DateHeaderMustBeSigned(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "http://examplebucket.s3.amazonaws.com/test.txt", nil)
	r.Header.Set("Date", "Fri, 24 May 2013 00:00:00 GMT")
	a := &sigV4Auth{AccessKey: docAccessKey, Scope: "20130524/us-east-1/s3/aws4_request", SignedHeaders: "host", Signature: "00"}
	if _, err := computeHeaderSignature(r, a, docSecretKey); !isUnsignedHeadersErr(err) {
		t.Fatalf("unsigned Date header: %v", err)
	}
}

func TestAbortUnsignedHeaders_AccessDenied(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	if abortUnsignedHeaders(c, errors.New("signature mismatch")) {
		t.Fatal("generic errors must not be mapped")
	}
	if !abortUnsignedHeaders(c, &unsignedHeadersError{names: []string{"x-amz-copy-source"}}) {
		t.Fatal("unsigned header error not mapped")
	}
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"AccessDenied"`) ||
		!strings.Contains(w.Body.String(), "not signed: x-amz-copy-source") {
		t.Fatalf("got %d %s", w.Code, w.Body.String())
	}
}
