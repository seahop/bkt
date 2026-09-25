package security

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// PolicyDocument represents an IAM-style policy document
type PolicyDocument struct {
	Version   string            `json:"Version"`
	Id        string            `json:"Id,omitempty"` // optional AWS policy identifier (informational only)
	Statement []PolicyStatement `json:"Statement"`
}

// PolicyStatement represents a single policy statement
type PolicyStatement struct {
	Sid       string                 `json:"Sid,omitempty"`       // Statement ID
	Effect    string                 `json:"Effect"`              // "Allow" or "Deny"
	Principal interface{}            `json:"Principal,omitempty"` // Optional: "*" or [usernames]. Absent = applies to all. Used by bucket policies to scope a statement to specific users.
	Action    []string               `json:"Action"`              // Actions this statement applies to
	Resource  []string               `json:"Resource"`            // Resources this statement applies to
	Condition map[string]interface{} `json:"Condition,omitempty"` // NOT evaluated — rejected on create/update (see validateStatement)

	// Unsupported IAM elements. They are declared (rather than left unknown) so
	// that a document using them is rejected with a clear message on
	// create/update, and so a legacy stored document that contains them is
	// evaluated fail-safe instead of having them silently dropped — dropping
	// NotAction/NotResource/NotPrincipal would make an Allow broader than
	// written.
	NotPrincipal interface{} `json:"NotPrincipal,omitempty"`
	NotAction    interface{} `json:"NotAction,omitempty"`
	NotResource  interface{} `json:"NotResource,omitempty"`
}

// unsupportedElements lists the statement elements bkt cannot evaluate
// faithfully (Condition and the Not* forms). Empty when the statement only
// uses supported elements.
func (s *PolicyStatement) unsupportedElements() []string {
	var out []string
	if len(s.Condition) > 0 {
		out = append(out, "Condition")
	}
	if s.NotPrincipal != nil {
		out = append(out, "NotPrincipal")
	}
	if s.NotAction != nil {
		out = append(out, "NotAction")
	}
	if s.NotResource != nil {
		out = append(out, "NotResource")
	}
	return out
}

// PolicyEffect represents the effect of a policy
type PolicyEffect string

const (
	EffectAllow PolicyEffect = "Allow"
	EffectDeny  PolicyEffect = "Deny"
)

// PolicyResult is the tri-state outcome of evaluating a policy document.
// Distinguishing NoMatch from Deny prevents a policy that simply doesn't
// cover an action from being mistaken for an explicit denial.
type PolicyResult int

const (
	PolicyNoMatch PolicyResult = iota // no statement matched — neither allow nor deny
	PolicyAllow                       // at least one Allow statement matched, no Deny
	PolicyDeny                        // at least one Deny statement matched
)

// PolicyEvaluationContext contains context for policy evaluation
type PolicyEvaluationContext struct {
	UserID     string
	Username   string // requesting user, for Principal matching
	Action     string
	Resource   string
	IsAdmin    bool
	Conditions map[string]string
}

// ValidatePolicyDocument strictly validates a policy document submitted for
// create/update (user, group, and bucket policies). Unknown elements are
// rejected (field names match case-insensitively, as encoding/json does), and
// so are elements bkt cannot evaluate faithfully — Condition, NotPrincipal,
// NotAction, NotResource — because silently ignoring them would grant more
// than the document says (e.g. an AWS "home folder" policy whose s3:prefix
// Condition would otherwise be dropped, granting the whole bucket).
func ValidatePolicyDocument(documentJSON string) (*PolicyDocument, error) {
	return parsePolicyDocument(documentJSON, true)
}

// ParseStoredPolicyDocument parses a policy document that is already stored
// (for evaluation). It is lenient about elements that strict validation
// rejects so that a legacy document is still evaluated — fail-safe — rather
// than skipped as a whole (skipping would also drop its Deny statements).
// EvaluatePolicy never lets an Allow statement with an unsupported element
// grant, and applies a Deny statement with one conservatively.
func ParseStoredPolicyDocument(documentJSON string) (*PolicyDocument, error) {
	return parsePolicyDocument(documentJSON, false)
}

func parsePolicyDocument(documentJSON string, strict bool) (*PolicyDocument, error) {
	// Check max size (prevent DoS via large policies)
	if len(documentJSON) > 10240 { // 10KB max
		return nil, fmt.Errorf("policy document too large (max 10KB)")
	}

	var policy PolicyDocument
	if strict {
		dec := json.NewDecoder(strings.NewReader(documentJSON))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&policy); err != nil {
			if strings.Contains(err.Error(), "unknown field") {
				return nil, fmt.Errorf("unsupported policy element: %w", err)
			}
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		// Reject trailing data (json.Unmarshal would; a Decoder stops after one value).
		if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("invalid JSON: unexpected data after the policy document")
		}
	} else if err := json.Unmarshal([]byte(documentJSON), &policy); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	// Validate version
	if policy.Version == "" {
		policy.Version = "2012-10-17" // AWS IAM default version
	}
	if policy.Version != "2012-10-17" {
		return nil, fmt.Errorf("unsupported policy version: %s", policy.Version)
	}

	// Must have at least one statement
	if len(policy.Statement) == 0 {
		return nil, fmt.Errorf("policy must contain at least one statement")
	}

	// Limit number of statements (prevent DoS)
	if len(policy.Statement) > 20 {
		return nil, fmt.Errorf("policy cannot contain more than 20 statements")
	}

	// Validate each statement
	for i := range policy.Statement {
		if err := validateStatement(&policy.Statement[i], strict); err != nil {
			return nil, fmt.Errorf("statement %d: %w", i, err)
		}
	}

	return &policy, nil
}

// validateStatement validates a single policy statement. In strict mode
// (create/update) statements using unsupported elements are rejected; in
// lenient mode (stored documents) they are accepted and EvaluatePolicy
// handles them fail-safe.
func validateStatement(stmt *PolicyStatement, strict bool) error {
	// Validate Effect
	if stmt.Effect != string(EffectAllow) && stmt.Effect != string(EffectDeny) {
		return fmt.Errorf("effect must be 'Allow' or 'Deny', got: %s", stmt.Effect)
	}

	unsupported := stmt.unsupportedElements()
	if strict && len(unsupported) > 0 {
		for _, el := range unsupported {
			if el == "Condition" {
				//nolint:staticcheck // ST1005: starts with the policy element name "Condition"
				return fmt.Errorf("Condition is not supported yet: remove the Condition block (bkt does not evaluate conditions, so the statement would apply more broadly than written)")
			}
		}
		return fmt.Errorf("%s is not supported: use Principal/Action/Resource instead", unsupported[0])
	}

	// Validate Action (must have at least one). A legacy stored statement
	// using NotAction may have none; it is handled by EvaluatePolicy.
	if len(stmt.Action) == 0 && (strict || stmt.NotAction == nil) {
		return fmt.Errorf("statement must have at least one action")
	}

	// Limit number of actions per statement (prevent DoS)
	if len(stmt.Action) > 50 {
		return fmt.Errorf("statement cannot contain more than 50 actions")
	}

	// Validate action format and prevent dangerous wildcards
	for _, action := range stmt.Action {
		if err := validateAction(action); err != nil {
			return fmt.Errorf("invalid action '%s': %w", action, err)
		}
		// Limit action string length (prevent DoS)
		if len(action) > 200 {
			return fmt.Errorf("action '%s' too long (max 200 characters)", action)
		}
	}

	// Validate Resource (must have at least one); same NotResource caveat.
	if len(stmt.Resource) == 0 && (strict || stmt.NotResource == nil) {
		return fmt.Errorf("statement must have at least one resource")
	}

	// Limit number of resources per statement (prevent DoS)
	if len(stmt.Resource) > 50 {
		return fmt.Errorf("statement cannot contain more than 50 resources")
	}

	// Validate resource format
	for _, resource := range stmt.Resource {
		if err := validateResource(resource); err != nil {
			return fmt.Errorf("invalid resource '%s': %w", resource, err)
		}
		// Limit resource string length (prevent DoS)
		if len(resource) > 500 {
			return fmt.Errorf("resource '%s' too long (max 500 characters)", resource)
		}
	}

	// Validate Sid (if present)
	if stmt.Sid != "" {
		if err := validateSid(stmt.Sid); err != nil {
			return fmt.Errorf("invalid Sid: %w", err)
		}
	}

	// Validate Principal (if present)
	if stmt.Principal != nil {
		if err := validatePrincipal(stmt.Principal); err != nil {
			return err
		}
	}

	// Validate Condition size (if present) - prevent DoS via large condition objects
	if stmt.Condition != nil {
		conditionJSON, err := json.Marshal(stmt.Condition)
		if err != nil {
			return fmt.Errorf("invalid condition object")
		}
		if len(conditionJSON) > 2048 {
			return fmt.Errorf("condition object too large (max 2KB)")
		}
	}

	return nil
}

// validateAction validates an action string
func validateAction(action string) error {
	if action == "" {
		return fmt.Errorf("action cannot be empty")
	}

	// Allow wildcard
	if action == "*" {
		return nil
	}

	// Action format: service:action (e.g., s3:GetObject, s3:*, objectstore:*)
	parts := strings.Split(action, ":")
	if len(parts) != 2 {
		return fmt.Errorf("action must be in format 'service:action'")
	}

	// Validate service name (alphanumeric only)
	if !isAlphanumeric(parts[0]) && parts[0] != "*" {
		return fmt.Errorf("invalid service name")
	}

	// Validate action name (alphanumeric, wildcard, or *)
	if !isAlphanumericOrWildcard(parts[1]) && parts[1] != "*" {
		return fmt.Errorf("invalid action name")
	}

	return nil
}

// validateResource validates a resource ARN or pattern
func validateResource(resource string) error {
	if resource == "" {
		return fmt.Errorf("resource cannot be empty")
	}

	// Allow wildcard
	if resource == "*" {
		return nil
	}

	// Resource should start with arn: or be a simple path
	// Format: arn:partition:service:region:account:resource
	// Or simple format: bucket/object
	if strings.HasPrefix(resource, "arn:") {
		return validateARN(resource)
	}

	// Simple "bucket/object" format. No ".." check: resources are matched as
	// strings (never used as filesystem paths), and ".." is legal in S3 keys.
	return nil
}

// validateARN validates an ARN format
func validateARN(arn string) error {
	parts := strings.Split(arn, ":")
	if len(parts) < 6 {
		return fmt.Errorf("invalid ARN format")
	}

	if parts[0] != "arn" {
		return fmt.Errorf("ARN must start with 'arn:'")
	}

	return nil
}

// validateSid validates a statement ID
func validateSid(sid string) error {
	// Sid should be alphanumeric with hyphens/underscores
	matched, err := regexp.MatchString("^[a-zA-Z0-9_-]+$", sid)
	if err != nil {
		return err
	}
	if !matched {
		return fmt.Errorf("policy Sid must contain only alphanumeric characters, hyphens, and underscores")
	}

	if len(sid) > 100 {
		return fmt.Errorf("policy Sid too long (max 100 characters)")
	}

	return nil
}

// validatePrincipal validates a Principal field: it must be a string ("*" or a
// username) or an array of such strings. The AWS object form ({"AWS": …}) is
// intentionally rejected — bkt principals are usernames.
func validatePrincipal(principal interface{}) error {
	switch p := principal.(type) {
	case string:
		if len(p) > 200 {
			return fmt.Errorf("principal too long (max 200 characters)")
		}
		return nil
	case []interface{}:
		if len(p) > 50 {
			return fmt.Errorf("statement cannot contain more than 50 principals")
		}
		for _, v := range p {
			s, ok := v.(string)
			if !ok {
				return fmt.Errorf("principal entries must be strings")
			}
			if len(s) > 200 {
				return fmt.Errorf("principal too long (max 200 characters)")
			}
		}
		return nil
	default:
		return fmt.Errorf("principal must be a string or an array of strings")
	}
}

// isAlphanumeric checks if a string contains only alphanumeric characters
func isAlphanumeric(s string) bool {
	matched, _ := regexp.MatchString("^[a-zA-Z0-9]+$", s)
	return matched
}

// isAlphanumericOrWildcard checks if a string contains only alphanumeric characters or wildcards
func isAlphanumericOrWildcard(s string) bool {
	matched, _ := regexp.MatchString("^[a-zA-Z0-9*]+$", s)
	return matched
}

// EvaluatePolicy evaluates a policy document against a context and returns a PolicyResult.
// PolicyDeny wins over PolicyAllow. PolicyNoMatch is returned when no statement covers
// the action+resource — callers must treat NoMatch as implicit deny.
func EvaluatePolicy(policy *PolicyDocument, ctx *PolicyEvaluationContext) PolicyResult {
	// Admin users bypass policy checks (superuser privilege)
	if ctx.IsAdmin {
		return PolicyAllow
	}

	result := PolicyNoMatch

	for i := range policy.Statement {
		statement := &policy.Statement[i]
		// Fail-safe handling of elements bkt cannot evaluate (only reachable
		// for legacy stored documents — new ones are rejected on write): an
		// Allow must never grant, since its Condition/Not* might have narrowed
		// it; a Deny is applied as if the unsupported element were absent
		// (i.e. at least as broadly as written).
		if len(statement.unsupportedElements()) > 0 {
			if statement.Effect == string(EffectDeny) && matchesDenyConservatively(statement, ctx) {
				return PolicyDeny
			}
			continue
		}
		// Principal scopes a statement to specific users (used by bucket policies);
		// absent Principal applies to everyone.
		if !matchesPrincipal(statement.Principal, ctx.Username) {
			continue
		}
		if !matchesAction(statement.Action, ctx.Action) {
			continue
		}
		if !matchesResource(statement.Resource, ctx.Resource) {
			continue
		}
		if statement.Effect == string(EffectDeny) {
			return PolicyDeny // explicit deny wins immediately
		}
		if statement.Effect == string(EffectAllow) {
			result = PolicyAllow
		}
	}

	return result
}

// matchesDenyConservatively decides whether a Deny statement carrying an
// unsupported element applies. Conditions are treated as always true;
// NotPrincipal/NotAction/NotResource are treated as matching everything
// (when the positive form is absent). This can only deny more, never less.
func matchesDenyConservatively(st *PolicyStatement, ctx *PolicyEvaluationContext) bool {
	if st.NotPrincipal == nil && !matchesPrincipal(st.Principal, ctx.Username) {
		return false
	}
	if len(st.Action) > 0 && !matchesAction(st.Action, ctx.Action) {
		return false
	}
	if len(st.Resource) > 0 && !matchesResource(st.Resource, ctx.Resource) {
		return false
	}
	return true
}

// matchesPrincipal reports whether a statement's Principal applies to the given
// user. A nil Principal applies to everyone. "*" matches everyone; otherwise the
// username must be listed. An unrecognized form fails closed (no match).
func matchesPrincipal(principal interface{}, username string) bool {
	if principal == nil {
		return true
	}
	switch p := principal.(type) {
	case string:
		return p == "*" || p == username
	case []interface{}:
		for _, v := range p {
			if s, ok := v.(string); ok && (s == "*" || s == username) {
				return true
			}
		}
		return false
	default:
		return false
	}
}

// matchesAction checks if an action matches any pattern in the list. Action
// matching is case-insensitive (consistent with AWS IAM) and supports `*`
// wildcards anywhere in the pattern (e.g. "s3:*", "s3:Get*", "*").
func matchesAction(patterns []string, action string) bool {
	a := strings.ToLower(action)
	for _, pattern := range patterns {
		if globMatch(strings.ToLower(pattern), a) {
			return true
		}
	}
	return false
}

// matchesResource checks if a resource matches any pattern in the list. Resource
// matching is case-SENSITIVE (object keys are case-sensitive in S3) and supports
// `*` wildcards anywhere (e.g. "bucket/*", "bucket/photos/*", "*").
func matchesResource(patterns []string, resource string) bool {
	for _, pattern := range patterns {
		if globMatch(pattern, resource) {
			return true
		}
	}
	return false
}

// globMatch reports whether s matches pattern, where `*` matches any (possibly
// empty) sequence of characters. There are no other metacharacters. Matching is
// exact when the pattern contains no `*`.
func globMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s // no wildcard → exact match
	}
	// First segment must be a literal prefix.
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	// Interior segments must appear in order.
	for _, seg := range parts[1 : len(parts)-1] {
		if seg == "" {
			continue
		}
		idx := strings.Index(s, seg)
		if idx < 0 {
			return false
		}
		s = s[idx+len(seg):]
	}
	// Last segment must be a literal suffix.
	return strings.HasSuffix(s, parts[len(parts)-1])
}

// GetDefaultDenyAllPolicy returns a policy that denies all access (for safety)
func GetDefaultDenyAllPolicy() *PolicyDocument {
	return &PolicyDocument{
		Version: "2012-10-17",
		Statement: []PolicyStatement{
			{
				Sid:      "DenyAll",
				Effect:   string(EffectDeny),
				Action:   []string{"*"},
				Resource: []string{"*"},
			},
		},
	}
}

// GetDefaultReadOnlyPolicy returns a basic read-only policy template
func GetDefaultReadOnlyPolicy() *PolicyDocument {
	return &PolicyDocument{
		Version: "2012-10-17",
		Statement: []PolicyStatement{
			{
				Sid:    "ReadOnlyAccess",
				Effect: string(EffectAllow),
				Action: []string{
					"s3:GetObject",
					"s3:ListBucket",
				},
				Resource: []string{"*"},
			},
		},
	}
}
