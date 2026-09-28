package api

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"bkt/internal/database"
	"bkt/internal/models"
	"bkt/internal/security"
	"bkt/internal/services"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// Bucket-level S3 routing: GET/PUT/POST/DELETE on /{bucket} (and the
// "/{bucket}/" form BucketOr sends here) carry the operation in a query
// sub-resource (?policy, ?acl, ?versioning, ...). Every request is
// dispatched explicitly: an implemented sub-resource goes to its handler, a
// well-known but unsupported one gets the answer AWS gives for "not
// configured" (or 501), and anything else is 501 NotImplemented — never a
// silent fallback to ListObjects (GET) or to the bucket-creation probe (PUT).

const s3XMLNS = "http://s3.amazonaws.com/doc/2006-03-01/"

// maxBucketPolicyBody bounds a PUT ?policy body (AWS limits bucket policies to
// 20 KB; bkt's validator additionally caps documents at 10 KB).
const maxBucketPolicyBody = 20 * 1024

// listingQueryParams are the query parameters ListObjects (V1/V2),
// ListObjectVersions (?versions) and ListMultipartUploads (?uploads) accept
// or harmlessly ignore. Any other key on a bucket GET is an unimplemented
// sub-resource (501) — it must never be answered with a listing. SigV4
// query-auth parameters (X-Amz-*) and the SDKs' x-id marker are handled by
// isSigningQueryParam.
var listingQueryParams = map[string]bool{
	// ListObjects / ListObjectsV2
	"list-type": true, "prefix": true, "delimiter": true, "marker": true,
	"max-keys": true, "continuation-token": true, "start-after": true,
	"fetch-owner": true, "encoding-type": true,
	"metadata": true, // MinIO extension (minio-go ListObjectsV2WithMetadata); ignored
	// ListObjectVersions
	"versions": true, "key-marker": true, "version-id-marker": true,
	// ListMultipartUploads
	"uploads": true, "upload-id-marker": true, "max-uploads": true,
}

// isSigningQueryParam reports whether a query key belongs to request signing
// (presigned X-Amz-* parameters) or SDK bookkeeping (x-id=<Operation>, added
// by aws-sdk-go-v2 / JS v3) rather than naming an S3 operation.
func isSigningQueryParam(k string) bool {
	lk := strings.ToLower(k)
	return strings.HasPrefix(lk, "x-amz-") || lk == "x-id"
}

// bucketSubresourceName returns the first (sorted, for determinism) query key
// that is not a signing parameter, or "" when there is none.
func bucketSubresourceName(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		if !isSigningQueryParam(k) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	return keys[0]
}

// unsupportedListingParam returns a query key that is neither a listing
// parameter nor a signing parameter ("" when all are acceptable).
func unsupportedListingParam(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		if !listingQueryParams[k] && !isSigningQueryParam(k) {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	return keys[0]
}

// notImplemented answers 501 NotImplemented for an unsupported bucket
// sub-resource / query parameter.
func (h *S3APIHandler) notImplemented(c *gin.Context, what string) {
	msg := "This operation is not implemented by bkt"
	if what != "" {
		msg = fmt.Sprintf("The %q bucket sub-resource is not implemented by bkt", what)
	}
	c.Header("x-amz-request-id", uuid.New().String())
	h.s3Error(c, "NotImplemented", msg, c.Param("bucket"), http.StatusNotImplemented)
}

// bucketStub describes a well-known bucket GET sub-resource bkt does not
// configure: either an empty 200 document or a 404 "not configured" error,
// exactly what AWS answers for a bucket without that configuration.
type bucketStub struct {
	body      string // 200 with this XML body when set
	code, msg string // otherwise 404 with this S3 error
}

var bucketGetStubs = map[string]bucketStub{
	"tagging":           {code: "NoSuchTagSet", msg: "The TagSet does not exist (bkt does not support bucket tags)"},
	"cors":              {code: "NoSuchCORSConfiguration", msg: "The CORS configuration does not exist"},
	"website":           {code: "NoSuchWebsiteConfiguration", msg: "The specified bucket does not have a website configuration"},
	"replication":       {code: "ReplicationConfigurationNotFoundError", msg: "The replication configuration was not found (bkt replication is configured in the console bucket settings)"},
	"ownershipControls": {code: "OwnershipControlsNotFoundError", msg: "The bucket ownership controls were not found"},
	"publicAccessBlock": {code: "NoSuchPublicAccessBlockConfiguration", msg: "The public access block configuration was not found"},
	"object-lock":       {code: "ObjectLockConfigurationNotFoundError", msg: "Object Lock configuration does not exist for this bucket"},
	"logging":           {body: `<BucketLoggingStatus xmlns="` + s3XMLNS + `"></BucketLoggingStatus>`},
	"notification":      {body: `<NotificationConfiguration xmlns="` + s3XMLNS + `"></NotificationConfiguration>`},
	"accelerate":        {body: `<AccelerateConfiguration xmlns="` + s3XMLNS + `"></AccelerateConfiguration>`},
	"requestPayment":    {body: `<RequestPaymentConfiguration xmlns="` + s3XMLNS + `"><Payer>BucketOwner</Payer></RequestPaymentConfiguration>`},
}

// bucketGetSubresources are the GET sub-resources answered after the
// ListBucket gate (see ListObjects), in dispatch order.
var bucketGetSubresources = []string{
	"location", "versioning", "acl", "encryption", "policyStatus", "lifecycle",
	"tagging", "cors", "website", "logging", "notification", "replication",
	"ownershipControls", "publicAccessBlock", "object-lock", "accelerate", "requestPayment",
}

// ListObjects handles GET /{bucket}: bucket sub-resources, ListObjectVersions
// (?versions), ListMultipartUploads (?uploads), and ListObjects V1/V2. It
// keeps its historical name because it is the route's entry point.
func (h *S3APIHandler) ListObjects(c *gin.Context) {
	q := c.Request.URL.Query()
	has := func(k string) bool { _, ok := q[k]; return ok }

	// Bucket policy: its own authorization (admin or s3:GetBucketPolicy),
	// independent of listing permission.
	if has("policy") {
		h.GetBucketPolicy(c)
		return
	}

	userUUID, authed := h.s3Caller(c)
	if !authed {
		return
	}

	sub := ""
	for _, k := range bucketGetSubresources {
		if has(k) {
			sub = k
			break
		}
	}
	// Anything that is neither a known sub-resource nor a listing parameter
	// is an unimplemented operation: 501, never a listing.
	if sub == "" {
		if k := unsupportedListingParam(q); k != "" {
			h.notImplemented(c, k)
			return
		}
	}

	// Everything below needs the bucket and at least list access (as before),
	// so it reveals nothing to callers who cannot list the bucket.
	bucketName := c.Param("bucket")
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		h.s3Error(c, "NoSuchBucket", "The specified bucket does not exist", bucketName, http.StatusNotFound)
		return
	}
	if allowed, _ := h.policyService.CheckBucketAccess(userUUID, bucketName, services.ActionListBucket); !allowed {
		h.s3Error(c, "AccessDenied", "Access Denied", bucketName, http.StatusForbidden)
		return
	}

	switch sub {
	case "":
		// fall through to the listing variants below
	case "location":
		region := h.bucketRegion()
		// AWS represents us-east-1 as an empty LocationConstraint.
		loc := region
		if loc == "us-east-1" {
			loc = ""
		}
		c.Header("x-amz-request-id", uuid.New().String())
		c.XML(http.StatusOK, LocationConstraintResult{Xmlns: s3XMLNS, Value: loc})
		return
	case "versioning":
		status := ""
		switch bucket.Versioning {
		case models.VersioningEnabled:
			status = "Enabled"
		case models.VersioningSuspended:
			status = "Suspended"
		}
		c.Header("x-amz-request-id", uuid.New().String())
		c.XML(http.StatusOK, VersioningConfigurationResult{Xmlns: s3XMLNS, Status: status})
		return
	case "lifecycle":
		h.GetBucketLifecycle(c)
		return
	case "acl":
		h.getBucketACL(c, &bucket)
		return
	case "encryption":
		h.getBucketEncryption(c, &bucket)
		return
	case "policyStatus":
		c.Header("x-amz-request-id", uuid.New().String())
		c.XML(http.StatusOK, policyStatusXML{Xmlns: s3XMLNS, IsPublic: bucket.IsPublic})
		return
	default:
		stub := bucketGetStubs[sub]
		c.Header("x-amz-request-id", uuid.New().String())
		if stub.body != "" {
			c.Data(http.StatusOK, "application/xml", []byte(xml.Header+stub.body))
			return
		}
		h.s3Error(c, stub.code, stub.msg, bucketName, http.StatusNotFound)
		return
	}

	if has("versions") {
		h.ListObjectVersions(c)
		return
	}
	if has("uploads") {
		h.ListMultipartUploadsHandler(c)
		return
	}
	h.listObjects(c, &bucket)
}

// dispatchBucketPut routes PUT /{bucket}?<sub-resource>. It returns false for
// a plain PUT /{bucket} (bucket-creation probe), which the caller handles.
func (h *S3APIHandler) dispatchBucketPut(c *gin.Context) bool {
	q := c.Request.URL.Query()
	sub := bucketSubresourceName(q)
	if _, ok := q["versioning"]; ok {
		sub = "versioning"
	} else if _, ok := q["lifecycle"]; ok {
		sub = "lifecycle"
	} else if _, ok := q["policy"]; ok {
		sub = "policy"
	}
	switch sub {
	case "":
		return false
	case "versioning":
		h.PutBucketVersioning(c)
	case "lifecycle":
		h.PutBucketLifecycle(c)
	case "policy":
		h.PutBucketPolicy(c)
	default:
		if _, authed := h.s3Caller(c); !authed {
			return true
		}
		if sub == "acl" {
			c.Header("x-amz-request-id", uuid.New().String())
			h.s3Error(c, "NotImplemented", "PutBucketAcl is not supported: bkt buckets are private; public read access is an admin setting in the console (Bucket settings → Public read access)", c.Param("bucket"), http.StatusNotImplemented)
			return true
		}
		h.notImplemented(c, sub)
	}
	return true
}

// HandleBucketDelete routes DELETE /{bucket} and DELETE /{bucket}/:
// ?policy and ?lifecycle are implemented, other sub-resources are 501, and a
// plain DELETE (DeleteBucket) stays console-only.
func (h *S3APIHandler) HandleBucketDelete(c *gin.Context) {
	q := c.Request.URL.Query()
	if _, ok := q["policy"]; ok {
		h.DeleteBucketPolicy(c)
		return
	}
	if _, ok := q["lifecycle"]; ok {
		h.DeleteBucketLifecycle(c)
		return
	}
	sub := bucketSubresourceName(q)
	if sub == "" {
		h.DeleteBucketNotSupported(c)
		return
	}
	if _, authed := h.s3Caller(c); !authed {
		return
	}
	h.notImplemented(c, sub)
}

// ── Bucket policy ───────────────────────────────────────────────────────────

type policyStatusXML struct {
	XMLName  xml.Name `xml:"PolicyStatus"`
	Xmlns    string   `xml:"xmlns,attr"`
	IsPublic bool     `xml:"IsPublic"`
}

// bucketForPolicy loads the bucket for a ?policy operation after checking
// the caller. It answers the error itself and returns false on failure.
func (h *S3APIHandler) bucketForPolicy(c *gin.Context) (*models.Bucket, uuid.UUID, bool) {
	userUUID, authed := h.s3Caller(c)
	if !authed {
		return nil, uuid.Nil, false
	}
	bucketName := c.Param("bucket")
	var bucket models.Bucket
	if err := database.DB.Where("name = ?", bucketName).First(&bucket).Error; err != nil {
		h.s3Error(c, "NoSuchBucket", "The specified bucket does not exist", bucketName, http.StatusNotFound)
		return nil, uuid.Nil, false
	}
	return &bucket, userUUID, true
}

// GetBucketPolicy handles GET /{bucket}?policy: the stored policy document as
// application/json (AWS GetBucketPolicy). Documents live in a jsonb column,
// so the JSON round-trips but whitespace and key order are normalized. Admin, or
// s3:GetBucketPolicy on the bucket (the same rule as the REST endpoint).
func (h *S3APIHandler) GetBucketPolicy(c *gin.Context) {
	bucket, userUUID, ok := h.bucketForPolicy(c)
	if !ok {
		return
	}
	if !authorizeBucketConfig(h.policyService, userUUID, bucket.Name, services.ActionGetBucketPolicy) {
		h.s3Error(c, "AccessDenied", "Access Denied", bucket.Name, http.StatusForbidden)
		return
	}
	var bp models.BucketPolicy
	if err := database.DB.Where("bucket_id = ?", bucket.ID).First(&bp).Error; err != nil {
		h.s3Error(c, "NoSuchBucketPolicy", "The bucket policy does not exist", bucket.Name, http.StatusNotFound)
		return
	}
	c.Header("x-amz-request-id", uuid.New().String())
	c.Data(http.StatusOK, "application/json", []byte(bp.PolicyDocument))
}

// PutBucketPolicy handles PUT /{bucket}?policy (body: the policy JSON).
// Admin only, like PUT /api/buckets/{name}/policy. The document is checked by
// the same strict validator as the REST endpoint and stored as sent (the
// same JSON comes back from GetBucketPolicy).
func (h *S3APIHandler) PutBucketPolicy(c *gin.Context) {
	bucket, userUUID, ok := h.bucketForPolicy(c)
	if !ok {
		return
	}
	if !c.GetBool("is_admin") {
		h.s3Error(c, "AccessDenied", "Access Denied: only a bkt admin can change bucket policies", bucket.Name, http.StatusForbidden)
		return
	}
	// Bounded read to EOF, so the SigV4 payload-hash check (which runs when
	// the body is fully read) still applies.
	body, err := readBoundedBody(c.Request.Body, maxBucketPolicyBody)
	if err != nil {
		if errors.Is(err, errRequestBodyTooLarge) {
			h.s3Error(c, "MaxMessageLengthExceeded", "Your request was too big (bucket policies are limited to 20 KB)", bucket.Name, http.StatusBadRequest)
			return
		}
		code, msg, status, _ := bodyFailure(&guardedBody{err: err})
		h.s3Error(c, code, msg, bucket.Name, status)
		return
	}
	doc := string(body)
	if _, err := security.ValidatePolicyDocument(doc); err != nil {
		h.s3Error(c, "MalformedPolicy", err.Error(), bucket.Name, http.StatusBadRequest)
		return
	}
	previous, _ := h.policyService.GetBucketPolicy(bucket.Name)
	if err := h.policyService.SetBucketPolicy(bucket.Name, doc); err != nil {
		h.s3Error(c, "InternalError", "Failed to store bucket policy", bucket.Name, http.StatusInternalServerError)
		return
	}
	auditBucketPolicyChange(c, userUUID, s3CallerUsername(c), bucket, "s3", previous, &doc)
	c.Header("x-amz-request-id", uuid.New().String())
	c.Status(http.StatusNoContent)
}

// DeleteBucketPolicy handles DELETE /{bucket}?policy. Admin only;
// idempotent (204 whether or not a policy existed).
func (h *S3APIHandler) DeleteBucketPolicy(c *gin.Context) {
	bucket, userUUID, ok := h.bucketForPolicy(c)
	if !ok {
		return
	}
	if !c.GetBool("is_admin") {
		h.s3Error(c, "AccessDenied", "Access Denied: only a bkt admin can change bucket policies", bucket.Name, http.StatusForbidden)
		return
	}
	previous, _ := h.policyService.GetBucketPolicy(bucket.Name)
	if err := h.policyService.DeleteBucketPolicy(bucket.Name); err != nil {
		h.s3Error(c, "InternalError", "Failed to delete bucket policy", bucket.Name, http.StatusInternalServerError)
		return
	}
	if previous != nil {
		auditBucketPolicyChange(c, userUUID, s3CallerUsername(c), bucket, "s3", previous, nil)
	}
	c.Header("x-amz-request-id", uuid.New().String())
	c.Status(http.StatusNoContent)
}

// auditBucketPolicyChange records a bucket-policy change (set when newDoc is
// non-nil, delete otherwise) with the previous and new documents.
func auditBucketPolicyChange(c *gin.Context, userID uuid.UUID, username string, bucket *models.Bucket, via string, previous *models.BucketPolicy, newDoc *string) {
	action := "bucket.policy.delete"
	meta := map[string]interface{}{"via": via}
	if newDoc != nil {
		action = "bucket.policy.set"
		meta["policy"] = *newDoc
	}
	if previous != nil {
		meta["previous_policy"] = previous.PolicyDocument
	}
	_ = services.NewAuditService().LogSuccess(c, userID, username, action, "bucket", bucket.ID.String(), bucket.Name, meta)
}

// s3CallerUsername is the authenticated caller's username (S3 auth sets
// "user"; the REST middleware and tests set "username").
func s3CallerUsername(c *gin.Context) string {
	if v, ok := c.Get("user"); ok {
		if u, ok := v.(*models.User); ok && u != nil && u.Username != "" {
			return u.Username
		}
	}
	return c.GetString("username")
}

// ── ACL / encryption ────────────────────────────────────────────────────────

type aclGranteeXML struct {
	XMLNSXsi    string `xml:"xmlns:xsi,attr"`
	Type        string `xml:"xsi:type,attr"`
	ID          string `xml:"ID,omitempty"`
	DisplayName string `xml:"DisplayName,omitempty"`
	URI         string `xml:"URI,omitempty"`
}

type aclGrantXML struct {
	Grantee    aclGranteeXML `xml:"Grantee"`
	Permission string        `xml:"Permission"`
}

type accessControlPolicyXML struct {
	XMLName xml.Name      `xml:"AccessControlPolicy"`
	Xmlns   string        `xml:"xmlns,attr"`
	Owner   Owner         `xml:"Owner"`
	Grants  []aclGrantXML `xml:"AccessControlList>Grant"`
}

const (
	xsiNamespace     = "http://www.w3.org/2001/XMLSchema-instance"
	allUsersGroupURI = "http://acs.amazonaws.com/groups/global/AllUsers"
)

// getBucketACL answers GET /{bucket}?acl with a read-only view derived from
// bkt's own settings: FULL_CONTROL for the bucket owner, plus READ for
// AllUsers while public read access (is_public) is on. bkt's public read
// covers object GET/HEAD only — not listing, which AWS's bucket READ grant
// would mean.
func (h *S3APIHandler) getBucketACL(c *gin.Context, bucket *models.Bucket) {
	c.Header("x-amz-request-id", uuid.New().String())
	c.XML(http.StatusOK, aclForBucket(bucket, bucket.IsPublic))
}

// aclForBucket builds the read-only ACL bkt reports for a bucket or one of
// its objects: FULL_CONTROL for the bucket owner, plus READ for AllUsers when
// public (bkt has no per-object ACLs or object owners).
func aclForBucket(bucket *models.Bucket, public bool) accessControlPolicyXML {
	var owner models.User
	_ = database.DB.Select("id", "username").First(&owner, "id = ?", bucket.OwnerID).Error
	ownerXML := Owner{ID: bucket.OwnerID.String(), DisplayName: owner.Username}
	out := accessControlPolicyXML{
		Xmlns: s3XMLNS,
		Owner: ownerXML,
		Grants: []aclGrantXML{{
			Grantee:    aclGranteeXML{XMLNSXsi: xsiNamespace, Type: "CanonicalUser", ID: ownerXML.ID, DisplayName: ownerXML.DisplayName},
			Permission: "FULL_CONTROL",
		}},
	}
	if public {
		out.Grants = append(out.Grants, aclGrantXML{
			Grantee:    aclGranteeXML{XMLNSXsi: xsiNamespace, Type: "Group", URI: allUsersGroupURI},
			Permission: "READ",
		})
	}
	return out
}

type sseConfigXML struct {
	XMLName xml.Name `xml:"ServerSideEncryptionConfiguration"`
	Xmlns   string   `xml:"xmlns,attr"`
	Rule    struct {
		Default struct {
			SSEAlgorithm string `xml:"SSEAlgorithm"`
		} `xml:"ApplyServerSideEncryptionByDefault"`
		BucketKeyEnabled bool `xml:"BucketKeyEnabled"`
	} `xml:"Rule"`
}

// getBucketEncryption answers GET /{bucket}?encryption: SSE-S3 (AES256) when
// the bucket lives on the S3 backend and S3_SSE is on (bkt then requests
// SSE on every write), otherwise the AWS "not configured" 404.
func (h *S3APIHandler) getBucketEncryption(c *gin.Context, bucket *models.Bucket) {
	c.Header("x-amz-request-id", uuid.New().String())
	if h.config == nil || !h.config.Storage.S3SSE || bucket.StorageBackend != "s3" {
		h.s3Error(c, "ServerSideEncryptionConfigurationNotFoundError", "The server side encryption configuration was not found", bucket.Name, http.StatusNotFound)
		return
	}
	out := sseConfigXML{Xmlns: s3XMLNS}
	out.Rule.Default.SSEAlgorithm = "AES256"
	c.XML(http.StatusOK, out)
}
