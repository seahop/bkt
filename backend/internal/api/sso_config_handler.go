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
}

// GetSSOConfig returns the SSO configuration for the frontend
func (h *SSOConfigHandler) GetSSOConfig(c *gin.Context) {
	response := SSOConfigResponse{
		GoogleEnabled: h.config.GoogleSSO.Enabled,
		VaultEnabled:  h.config.VaultSSO.Enabled,
	}

	// Only include auth URL if Google SSO is enabled
	if h.config.GoogleSSO.Enabled {
		response.GoogleAuthURL = "/api/auth/google/login"
	}

	c.JSON(http.StatusOK, response)
}
