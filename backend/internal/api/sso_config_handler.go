package api

import (
	"net/http"
	"bkt/internal/config"

	"github.com/gin-gonic/gin"
)

type SSOConfigHandler struct {
	config *config.Config
}

func NewSSOConfigHandler(cfg *config.Config) *SSOConfigHandler {
	return &SSOConfigHandler{config: cfg}
}

// SSOConfigResponse represents the SSO configuration available to clients
type SSOConfigResponse struct {
	GoogleEnabled bool   `json:"google_enabled"`
	GoogleAuthURL string `json:"google_auth_url,omitempty"`
	VaultEnabled  bool   `json:"vault_enabled"`
	VaultAuthURL  string `json:"vault_auth_url,omitempty"`
}

// GetSSOConfig returns the SSO configuration for the frontend
func (h *SSOConfigHandler) GetSSOConfig(c *gin.Context) {
	// Vault is enabled if either legacy JWT or OIDC mode is enabled
	vaultEnabled := h.config.VaultSSO.Enabled || h.config.VaultSSO.OIDCEnabled

	response := SSOConfigResponse{
		GoogleEnabled: h.config.GoogleSSO.OIDCEnabled,
		VaultEnabled:  vaultEnabled,
	}

	// Only include auth URL if Google OIDC is enabled
	if h.config.GoogleSSO.OIDCEnabled {
		response.GoogleAuthURL = "/api/auth/google/login"
	}

	// Include Vault auth URL if OIDC is enabled (browser-based flow)
	if h.config.VaultSSO.OIDCEnabled {
		response.VaultAuthURL = "/api/auth/vault/login"
	}

	c.JSON(http.StatusOK, response)
}
