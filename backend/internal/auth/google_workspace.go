package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/models"

	"golang.org/x/oauth2/google"
	admin "google.golang.org/api/admin/directory/v1"
	"google.golang.org/api/option"
)

// GoogleWorkspaceService handles Google Workspace API interactions
type GoogleWorkspaceService struct {
	config *config.Config

	// managed caches the set of bkt policy names the group→policy mapping
	// can produce from the domain's groups (see GetManagedPolicyNames).
	managedMu sync.Mutex
	managed   map[string]bool
	managedAt time.Time
}

// managedPolicyTTL bounds how long the domain-wide group list is reused.
const managedPolicyTTL = 10 * time.Minute

// NewGoogleWorkspaceService creates a new Google Workspace service
func NewGoogleWorkspaceService(cfg *config.Config) *GoogleWorkspaceService {
	return &GoogleWorkspaceService{config: cfg}
}

// GetUserGroups fetches all groups a user belongs to via Google Workspace Admin SDK
func (s *GoogleWorkspaceService) GetUserGroups(ctx context.Context, userEmail string) ([]string, error) {
	if !s.config.GoogleSSO.WorkspaceEnabled {
		return nil, nil
	}
	adminService, err := s.directory(ctx)
	if err != nil {
		return nil, err
	}

	// Fetch groups for the user
	var groups []string
	pageToken := ""

	for {
		call := adminService.Groups.List().UserKey(userEmail)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}

		result, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("failed to fetch groups for user %s: %w", userEmail, err)
		}

		for _, group := range result.Groups {
			// Extract group name (email prefix) or full email based on config
			groupName := extractGroupName(group.Email)
			groups = append(groups, groupName)
		}

		pageToken = result.NextPageToken
		if pageToken == "" {
			break
		}
	}

	return groups, nil
}

// GetManagedPolicyNames returns the set of policy names the group→policy
// mapping produces from ALL of the domain's groups — the policies Workspace
// "owns". Login-time sync only adds/removes policies in this set, leaving
// policies an administrator assigned by hand untouched. Cached for
// managedPolicyTTL.
func (s *GoogleWorkspaceService) GetManagedPolicyNames(ctx context.Context) (map[string]bool, error) {
	s.managedMu.Lock()
	defer s.managedMu.Unlock()
	if s.managed != nil && time.Since(s.managedAt) < managedPolicyTTL {
		return s.managed, nil
	}
	adminService, err := s.directory(ctx)
	if err != nil {
		return nil, err
	}
	var groups []string
	pageToken := ""
	for {
		call := adminService.Groups.List().Customer("my_customer").MaxResults(200)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		result, err := call.Do()
		if err != nil {
			return nil, fmt.Errorf("failed to list Workspace groups: %w", err)
		}
		for _, g := range result.Groups {
			groups = append(groups, extractGroupName(g.Email))
		}
		pageToken = result.NextPageToken
		if pageToken == "" {
			break
		}
	}
	managed := make(map[string]bool)
	for _, n := range s.GetPolicyNamesFromGroups(groups) {
		managed[n] = true
	}
	s.managed, s.managedAt = managed, time.Now()
	return managed, nil
}

// directory builds an Admin SDK Directory client using the service account
// with domain-wide delegation (impersonating GOOGLE_WORKSPACE_ADMIN_EMAIL).
func (s *GoogleWorkspaceService) directory(ctx context.Context) (*admin.Service, error) {
	// Load service account credentials
	keyFile := s.config.GoogleSSO.ServiceAccountKeyFile
	keyData, err := os.ReadFile(keyFile) //nolint:gosec // path from server-side config (service account key file), not user input
	if err != nil {
		return nil, fmt.Errorf("failed to read service account key file: %w", err)
	}

	// Create JWT config with domain-wide delegation
	// The admin email is used for impersonation (required for domain-wide delegation)
	jwtConfig, err := google.JWTConfigFromJSON(keyData, admin.AdminDirectoryGroupReadonlyScope)
	if err != nil {
		return nil, fmt.Errorf("failed to parse service account key: %w", err)
	}

	// Set the subject (admin user to impersonate)
	jwtConfig.Subject = s.config.GoogleSSO.WorkspaceAdminEmail

	// Create the Admin SDK client
	client := jwtConfig.Client(ctx)
	adminService, err := admin.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("failed to create Admin SDK client: %w", err)
	}
	return adminService, nil
}

// GetPolicyNamesFromGroups maps group names to policy names based on config
func (s *GoogleWorkspaceService) GetPolicyNamesFromGroups(groups []string) []string {
	var policyNames []string
	prefix := s.config.GoogleSSO.PolicyGroupPrefix

	for _, group := range groups {
		var policyName string

		switch s.config.GoogleSSO.PolicySyncMode {
		case "prefix":
			// Only include groups that start with the prefix
			// e.g., prefix="bkt-", group="bkt-engineering" -> policy="engineering"
			if prefix != "" && strings.HasPrefix(group, prefix) {
				policyName = strings.TrimPrefix(group, prefix)
			}
		default: // "direct"
			// Group name = policy name (optionally filtered by prefix)
			if prefix == "" {
				policyName = group
			} else if strings.HasPrefix(group, prefix) {
				// If prefix is set in direct mode, only use matching groups
				policyName = group
			}
		}

		if policyName != "" {
			policyNames = append(policyNames, policyName)
		}
	}

	return policyNames
}

// SyncUserPoliciesFromGroups applies the policies mapped from the user's
// Google Workspace groups, managing ONLY Workspace-mapped policies: every
// policy in managed (plus the ones mapped for this user) is added or removed
// to match the user's current groups — so leaving a group revokes its
// policy — while policies outside that set (assigned manually by an
// administrator) are left untouched.
func (s *GoogleWorkspaceService) SyncUserPoliciesFromGroups(user *models.User, policyNames []string, managed map[string]bool) error {
	desired := []models.Policy{}
	if len(policyNames) > 0 {
		if err := database.DB.Where("name IN ?", policyNames).Find(&desired).Error; err != nil {
			return fmt.Errorf("failed to look up policies: %w", err)
		}
	}
	var current []models.Policy
	if err := database.DB.Model(user).Association("Policies").Find(&current); err != nil {
		return fmt.Errorf("failed to load current policies: %w", err)
	}
	result := mergeManagedPolicies(current, desired, policyNames, managed)
	if len(result) == 0 {
		if err := database.DB.Model(user).Association("Policies").Clear(); err != nil {
			return fmt.Errorf("failed to sync policies: %w", err)
		}
		return nil
	}
	if err := database.DB.Model(user).Association("Policies").Replace(result); err != nil {
		return fmt.Errorf("failed to sync policies: %w", err)
	}
	return nil
}

// mergeManagedPolicies computes the user's new policy set: current policies
// that Workspace does not manage are kept; managed ones are replaced by the
// desired set. A name is managed when it is in managed or among the names
// mapped for this user.
func mergeManagedPolicies(current, desired []models.Policy, desiredNames []string, managed map[string]bool) []models.Policy {
	isManaged := func(name string) bool {
		if managed[name] {
			return true
		}
		for _, n := range desiredNames {
			if n == name {
				return true
			}
		}
		return false
	}
	seen := map[string]bool{}
	out := []models.Policy{}
	for _, p := range current {
		if isManaged(p.Name) || seen[p.ID.String()] {
			continue
		}
		seen[p.ID.String()] = true
		out = append(out, p)
	}
	for _, p := range desired {
		if seen[p.ID.String()] {
			continue
		}
		seen[p.ID.String()] = true
		out = append(out, p)
	}
	return out
}

// extractGroupName extracts the group name from an email address
// e.g., "engineering@company.com" -> "engineering"
func extractGroupName(groupEmail string) string {
	parts := strings.Split(groupEmail, "@")
	if len(parts) > 0 {
		return parts[0]
	}
	return groupEmail
}

// ServiceAccountKey represents the structure of a Google service account JSON key
type ServiceAccountKey struct {
	Type                    string `json:"type"`
	ProjectID               string `json:"project_id"`
	PrivateKeyID            string `json:"private_key_id"`
	ClientEmail             string `json:"client_email"`
	AuthURI                 string `json:"auth_uri"`
	TokenURI                string `json:"token_uri"`
	AuthProviderX509CertURL string `json:"auth_provider_x509_cert_url"`
	ClientX509CertURL       string `json:"client_x509_cert_url"`
}

// ValidateServiceAccountKey checks if the service account key file is valid
func ValidateServiceAccountKey(keyFilePath string) (*ServiceAccountKey, error) {
	data, err := os.ReadFile(keyFilePath) //nolint:gosec // path from server-side config (service account key file), not user input
	if err != nil {
		return nil, fmt.Errorf("cannot read key file: %w", err)
	}

	var key ServiceAccountKey
	if err := json.Unmarshal(data, &key); err != nil {
		return nil, fmt.Errorf("invalid JSON format: %w", err)
	}

	if key.Type != "service_account" {
		return nil, fmt.Errorf("key file must be of type 'service_account', got '%s'", key.Type)
	}

	if key.ClientEmail == "" {
		return nil, fmt.Errorf("key file missing client_email")
	}

	return &key, nil
}
