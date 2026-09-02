package middleware

import (
	"net/http"
	"bkt/internal/auth"
	"bkt/internal/database"
	"bkt/internal/models"
	"strings"

	"github.com/gin-gonic/gin"
)

// AuthMiddleware validates JWT tokens and checks the revocation blacklist
func AuthMiddleware(jwtSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Authorization header required"})
			c.Abort()
			return
		}

		// Expected format: "Bearer <token>"
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid authorization format"})
			c.Abort()
			return
		}

		token := parts[1]
		claims, err := auth.ValidateToken(token, jwtSecret)
		if err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
			c.Abort()
			return
		}

		// Refresh tokens must not be usable as access tokens on protected
		// routes — only /auth/refresh accepts them. (Tokens issued before this
		// field existed have an empty type; treat only an explicit refresh type
		// as disallowed to avoid breaking already-issued access tokens.)
		if claims.TokenType == auth.TokenTypeRefresh {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
			c.Abort()
			return
		}

		// Check if token has been revoked (logout blacklist)
		if claims.ID != "" {
			var revoked models.RevokedToken
			if err := database.DB.Where("jti = ?", claims.ID).First(&revoked).Error; err == nil {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
				c.Abort()
				return
			}
		}

		// Re-read the user so that lock, deletion, admin demotion, and
		// "sign out everywhere" take effect immediately rather than at token
		// expiry. The token's TokenVersion must still match the user's current
		// version, and is_admin is taken from the live row (not the token).
		var user models.User
		if err := database.DB.Where("id = ?", claims.UserID).First(&user).Error; err != nil {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
			c.Abort()
			return
		}
		if user.IsLocked || user.TokenVersion != claims.TokenVersion {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "Invalid or expired token"})
			c.Abort()
			return
		}

		// Set user info in context (authoritative, from the live row)
		c.Set("user_id", user.ID)
		c.Set("username", user.Username)
		c.Set("is_admin", user.IsAdmin)
		c.Set("token_jti", claims.ID)
		c.Set("token_expires_at", claims.ExpiresAt.Time)
		c.Set("token_pair_jti", claims.PairJTI)

		c.Next()
	}
}

// AdminMiddleware ensures the user is an admin
func AdminMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		isAdmin, exists := c.Get("is_admin")
		if !exists || !isAdmin.(bool) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Admin access required"})
			c.Abort()
			return
		}
		c.Next()
	}
}
