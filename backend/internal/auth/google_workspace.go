package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

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
}

// NewGoogleWorkspaceService creates a new Google Workspace service
func NewGoogleWorkspaceService(cfg *config.Config) *GoogleWorkspaceService {
	return &GoogleWorkspaceService{config: cfg}
}

// GetUserGroups fetches all groups a user belongs to via Google Workspace Admin SDK
func (s *GoogleWorkspaceService) GetUserGroups(ctx context.Context, userEmail string) ([]string, error) {
	if !s.config.GoogleSSO.WorkspaceEnabled {
		return nil, nil
	}

	// Load service account credentials
	keyFile := s.config.GoogleSSO.ServiceAccountKeyFile
	keyData, err := os.ReadFile(keyFile)
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

// SyncUserPoliciesFromGroups syncs user policies based on their Google Workspace groups
func (s *GoogleWorkspaceService) SyncUserPoliciesFromGroups(user *models.User, policyNames []string) error {
	if len(policyNames) == 0 {
		return nil
	}

	// Look up policies by name
	var policies []models.Policy
	result := database.DB.Where("name IN ?", policyNames).Find(&policies)
	if result.Error != nil {
		return fmt.Errorf("failed to look up policies: %w", result.Error)
	}

	// Log which policies were found vs requested (helpful for debugging)
	if len(policies) != len(policyNames) {
		foundNames := make([]string, len(policies))
		for i, p := range policies {
			foundNames[i] = p.Name
		}
		// Debug logging (uncomment for troubleshooting)
		// fmt.Printf("[GoogleWorkspace] Policy sync for %s: requested %v, found %v\n",
		// 	user.Email, policyNames, foundNames)
	}

	// Replace user's policies with those from Google Workspace groups
	if err := database.DB.Model(user).Association("Policies").Replace(policies); err != nil {
		return fmt.Errorf("failed to sync policies: %w", err)
	}

	return nil
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
	data, err := os.ReadFile(keyFilePath)
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
