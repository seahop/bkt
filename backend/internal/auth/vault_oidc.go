package auth

import "bkt/internal/config"

// NewVaultOIDCHandler builds the historical VAULT_OIDC_* provider slot on top of
// the generic OIDC handler. It keeps the original route (/api/auth/vault/*),
// cookie names, frontend callback and users.sso_provider="vault" value, so
// existing deployments keep working unchanged. New integrations should use the
// OIDC_* settings instead (see NewOIDCHandler), which add claims mapping.
func NewVaultOIDCHandler(cfg *config.Config) *OIDCHandler {
	v := cfg.VaultSSO
	h := newOIDCHandler(cfg, OIDCProviderSettings{
		Key:                  "vault",
		DisplayName:          "Vault",
		AuditName:            "vault-oidc",
		IssuerURL:            v.ProviderURL,
		ClientID:             v.ClientID,
		RedirectURL:          v.RedirectURL,
		Scopes:               v.Scopes,
		FrontendCallbackPath: "/auth/vault/callback",
		CookiePrefix:         "vault_",
		PoliciesClaim:        "policies",
		VaultLegacyURLs:      true,
	})
	if !v.OIDCEnabled {
		// Honour the explicit switch: an unset VAULT_OIDC_ENABLED disables the
		// slot even if a provider URL is present.
		h.s.IssuerURL = ""
	}
	return h
}
