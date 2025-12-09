package security

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// PolicyDocument represents an IAM-style policy document
type PolicyDocument struct {
	Version   string            `json:"Version"`
	Statement []PolicyStatement `json:"Statement"`
}

// PolicyStatement represents a single policy statement
type PolicyStatement struct {
	Sid       string                 `json:"Sid,omitempty"`       // Statement ID
	Effect    string                 `json:"Effect"`              // "Allow" or "Deny"
	Action    []string               `json:"Action"`              // Actions this statement applies to
	Resource  []string               `json:"Resource"`            // Resources this statement applies to
	Condition map[string]interface{} `json:"Condition,omitempty"` // Conditions for the statement
}

// PolicyEffect represents the effect of a policy
type PolicyEffect string

const (
	EffectAllow PolicyEffect = "Allow"
	EffectDeny  PolicyEffect = "Deny"
)

// PolicyEvaluationContext contains context for policy evaluation
type PolicyEvaluationContext struct {
	UserID     string
	Action     string
	Resource   string
	IsAdmin    bool
	Conditions map[string]string
}

// ValidatePolicyDocument validates a policy document for security and correctness
func ValidatePolicyDocument(documentJSON string) (*PolicyDocument, error) {
	// Check max size (prevent DoS via large policies)
	if len(documentJSON) > 10240 { // 10KB max
		return nil, fmt.Errorf("policy document too large (max 10KB)")
	}

	var policy PolicyDocument
	if err := json.Unmarshal([]byte(documentJSON), &policy); err != nil {
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
	for i, statement := range policy.Statement {
		if err := validateStatement(&statement, i); err != nil {
			return nil, fmt.Errorf("statement %d: %w", i, err)
		}
	}

	return &policy, nil
}

// validateStatement validates a single policy statement
func validateStatement(stmt *PolicyStatement, index int) error {
	// Validate Effect
	if stmt.Effect != string(EffectAllow) && stmt.Effect != string(EffectDeny) {
		return fmt.Errorf("effect must be 'Allow' or 'Deny', got: %s", stmt.Effect)
	}

	// Validate Action (must have at least one)
	if len(stmt.Action) == 0 {
		return fmt.Errorf("statement must have at least one action")
	}

	// Validate action format and prevent dangerous wildcards
	for _, action := range stmt.Action {
		if err := validateAction(action); err != nil {
			return fmt.Errorf("invalid action '%s': %w", action, err)
		}
	}

	// Validate Resource (must have at least one)
	if len(stmt.Resource) == 0 {
		return fmt.Errorf("statement must have at least one resource")
	}

	// Validate resource format
	for _, resource := range stmt.Resource {
		if err := validateResource(resource); err != nil {
			return fmt.Errorf("invalid resource '%s': %w", resource, err)
		}
	}

	// Validate Sid (if present)
	if stmt.Sid != "" {
		if err := validateSid(stmt.Sid); err != nil {
			return fmt.Errorf("invalid Sid: %w", err)
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

	// Simple format validation - no path traversal
	if strings.Contains(resource, "..") {
		return fmt.Errorf("resource cannot contain '..'")
	}

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
		return fmt.Errorf("Sid must contain only alphanumeric characters, hyphens, and underscores")
	}

	if len(sid) > 100 {
		return fmt.Errorf("Sid too long (max 100 characters)")
	}

	return nil
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

// EvaluatePolicy evaluates a policy document against a context
// Returns true if access is allowed, false if denied
// DENY-BY-DEFAULT: Returns false if no explicit allow is found
// EXPLICIT DENY WINS: If any deny is found, access is denied regardless of allows
func EvaluatePolicy(policy *PolicyDocument, ctx *PolicyEvaluationContext) bool {
	// Admin users bypass policy checks (superuser privilege)
	if ctx.IsAdmin {
		return true
	}

	hasExplicitAllow := false
	hasExplicitDeny := false

	// Evaluate each statement
	for _, statement := range policy.Statement {
		// Check if statement applies to this action
		if !matchesAction(statement.Action, ctx.Action) {
			continue
		}

		// Check if statement applies to this resource
		if !matchesResource(statement.Resource, ctx.Resource) {
			continue
		}

		// Statement applies - check effect
		if statement.Effect == string(EffectDeny) {
			hasExplicitDeny = true
			// Explicit deny wins - no need to check further
			break
		} else if statement.Effect == string(EffectAllow) {
			hasExplicitAllow = true
		}
	}

	// DENY OVERRIDES ALLOW
	if hasExplicitDeny {
		return false
	}

	// DENY BY DEFAULT - only allow if explicit allow found
	return hasExplicitAllow
}

// matchesAction checks if an action matches any pattern in the list
func matchesAction(patterns []string, action string) bool {
	for _, pattern := range patterns {
		if pattern == "*" {
			return true
		}
		if pattern == action {
			return true
		}
		// Handle wildcards like "s3:*"
		if strings.HasSuffix(pattern, ":*") {
			service := strings.TrimSuffix(pattern, ":*")
			if strings.HasPrefix(action, service+":") {
				return true
			}
		}
	}
	return false
}

// matchesResource checks if a resource matches any pattern in the list
func matchesResource(patterns []string, resource string) bool {
	for _, pattern := range patterns {
		if pattern == "*" {
			return true
		}
		if pattern == resource {
			return true
		}
		// Handle wildcards like "bucket/*"
		if strings.HasSuffix(pattern, "/*") {
			prefix := strings.TrimSuffix(pattern, "/*")
			if strings.HasPrefix(resource, prefix+"/") {
				return true
			}
		}
	}
	return false
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
					"objectstore:GetObject",
					"objectstore:ListBucket",
				},
				Resource: []string{"*"},
			},
		},
	}
}
