package api

import (
	"time"

	authpkg "bkt/internal/auth"
	"bkt/internal/config"
	_ "bkt/docs/swagger" // swaggo generated docs
	"bkt/internal/middleware"
	"bkt/internal/web"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"
)

// @title bkt API
// @version 1.0
// @description Self-hosted S3-compatible object storage gateway
// @contact.name bkt project
// @contact.url https://bkt.tips
// @license.name Apache 2.0
// @host localhost:9443
// @BasePath /
// @securityDefinitions.apikey BearerAuth
// @in header
// @name Authorization

// SetupConsoleRouter builds the browser-facing listener: the embedded web UI at
// `/`, the REST API under `/api`, Prometheus metrics, Swagger, and health
// probes. It deliberately has no `/:bucket` S3 routes, so the SPA can safely own
// every unmatched path via NoRoute.
func SetupConsoleRouter(cfg *config.Config) *gin.Engine {
	router := gin.Default()

	// Metrics + Swagger are registered before the middleware chain so they are
	// not subject to CORS/UA validation (standard for scraping & docs).
	router.GET("/metrics", gin.WrapH(promhttp.Handler()))
	router.GET("/api/docs/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))

	router.Use(middleware.RequestIDMiddleware())
	router.Use(middleware.MetricsMiddleware())
	router.Use(middleware.UserAgentValidationMiddleware())

	// CORS — browser-facing only. With the UI served same-origin from this
	// listener, this matters mainly for S3 clients / tooling hitting the API.
	router.Use(cors.New(cors.Config{
		AllowOrigins:     cfg.CORS.AllowedOrigins,
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "HEAD"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "X-Amz-Date", "X-Amz-Content-Sha256", "X-Request-ID", "Idempotency-Key"},
		ExposeHeaders:    []string{"Content-Length", "ETag", "X-Amz-Request-Id", "X-Request-ID"},
		AllowCredentials: cfg.CORS.AllowCredentials,
	}))

	// Health check endpoints
	router.GET("/health", HealthHandler)   // Full health with DB check
	router.GET("/ready", ReadinessHandler) // Readiness probe (for k8s)
	router.GET("/live", LivenessHandler)   // Liveness probe (for k8s)

	registerAPIRoutes(router, cfg)

	// Embedded single-page app as the catch-all (must be last).
	web.RegisterUI(router)

	return router
}

// SetupS3Router builds the S3-compatible API listener. S3 clients (aws-cli,
// s3fs) address buckets at the host root, so these routes own `/` and cannot
// share a listener with the web UI.
func SetupS3Router(cfg *config.Config) *gin.Engine {
	router := gin.Default()

	router.Use(middleware.RequestIDMiddleware())
	router.Use(middleware.MetricsMiddleware())
	router.Use(middleware.UserAgentValidationMiddleware())

	s3Handler := NewS3APIHandler(cfg)
	s3 := router.Group("")
	s3.Use(middleware.S3AuthMiddleware())
	{
		// Service-level operations
		s3.GET("/", s3Handler.ListBuckets)

		// Bucket-level operations
		s3.HEAD("/:bucket", s3Handler.HeadBucket)
		s3.GET("/:bucket", s3Handler.ListObjects)
		s3.POST("/:bucket", s3Handler.HandleBucketPost) // e.g. ?delete for bulk delete
		s3.PUT("/:bucket", s3Handler.CreateBucket)      // Currently disabled

		// Object-level operations
		s3.HEAD("/:bucket/*key", s3Handler.HeadObject)
		s3.GET("/:bucket/*key", s3Handler.GetObject)         // also handles ListParts (?uploadId)
		s3.PUT("/:bucket/*key", s3Handler.PutObject)         // also handles UploadPart (?partNumber&uploadId)
		s3.POST("/:bucket/*key", s3Handler.HandleObjectPost) // CreateMultipartUpload (?uploads) or CompleteMultipartUpload (?uploadId)
		s3.DELETE("/:bucket/*key", s3Handler.DeleteObject)   // also handles AbortMultipartUpload (?uploadId)
	}

	return router
}

// registerAPIRoutes wires the JWT-authenticated REST API under /api onto the
// given engine.
func registerAPIRoutes(router *gin.Engine, cfg *config.Config) {
	api := router.Group("/api")
	{
		// Auth routes (no authentication required)
		authHandler := NewAuthHandler(cfg)
		auth := api.Group("/auth")
		{
			// Auth rate limiting — configurable via AUTH_RATE_LIMIT env var.
			// Default: 5/min (production). Set higher (e.g. 60) for testing/dev.
			authRatePerMin := cfg.Auth.AuthRateLimit
			if authRatePerMin <= 0 {
				authRatePerMin = 5
			}
			authRateLimit := middleware.RateLimitMiddleware(authRatePerMin, time.Minute)

			auth.POST("/register", authRateLimit, authHandler.Register)
			auth.POST("/login", authRateLimit, authHandler.Login)
			auth.POST("/refresh", authRateLimit, authHandler.RefreshToken)

			// SSO configuration endpoint
			ssoConfigHandler := NewSSOConfigHandler(cfg)
			auth.GET("/sso/config", ssoConfigHandler.GetSSOConfig)

			// Google OAuth routes
			googleHandler := authpkg.NewGoogleOAuthHandler(cfg)
			auth.GET("/google/login", googleHandler.InitiateGoogleLogin)
			auth.GET("/google/callback", googleHandler.HandleGoogleCallback)

			// Vault JWT routes (legacy token-based login)
			vaultJWTHandler := authpkg.NewVaultJWTHandler(cfg)
			auth.POST("/vault/login", vaultJWTHandler.LoginWithVaultJWT)

			// Vault OIDC routes (browser-based SSO with PKCE)
			vaultOIDCHandler := authpkg.NewVaultOIDCHandler(cfg)
			auth.GET("/vault/login", vaultOIDCHandler.InitiateVaultLogin)
			auth.GET("/vault/callback", vaultOIDCHandler.HandleVaultCallback)
		}

		// Protected routes (require authentication)
		protected := api.Group("")
		protected.Use(middleware.AuthMiddleware(cfg.Auth.JWTSecret))
		protected.Use(middleware.IdempotencyMiddleware()) // Apply idempotency to all authenticated routes
		{
			// User routes
			userHandler := NewUserHandler(cfg)
			users := protected.Group("/users")
			{
				users.GET("/me", userHandler.GetCurrentUser)
				users.PUT("/me", userHandler.UpdateCurrentUser)
				users.GET("", middleware.AdminMiddleware(), userHandler.ListUsers)
				users.POST("", middleware.AdminMiddleware(), userHandler.CreateUser)
				users.DELETE("/:id", middleware.AdminMiddleware(), userHandler.DeleteUser)
				users.POST("/:id/lock", middleware.AdminMiddleware(), userHandler.LockUser)
				users.POST("/:id/unlock", middleware.AdminMiddleware(), userHandler.UnlockUser)
				users.GET("/:id/access-keys", middleware.AdminMiddleware(), userHandler.ListUserAccessKeys)
				users.DELETE("/:id/access-keys/:key_id", middleware.AdminMiddleware(), userHandler.DeleteUserAccessKey)
			}

			// Access key routes
			accessKeyHandler := NewAccessKeyHandler(cfg)
			accessKeys := protected.Group("/access-keys")
			{
				accessKeys.GET("", accessKeyHandler.ListAccessKeys)
				accessKeys.POST("", accessKeyHandler.GenerateAccessKey)
				accessKeys.DELETE("/:id", accessKeyHandler.RevokeAccessKey)
				accessKeys.GET("/stats", accessKeyHandler.GetAccessKeyStats)
			}

			// Bucket routes
			bucketHandler := NewBucketHandler(cfg)
			buckets := protected.Group("/buckets")
			{
				buckets.GET("", bucketHandler.ListBuckets)
				buckets.POST("", middleware.AdminMiddleware(), bucketHandler.CreateBucket) // Admin only
				buckets.GET("/:name", bucketHandler.GetBucket)
				buckets.DELETE("/:name", middleware.AdminMiddleware(), bucketHandler.DeleteBucket)       // Admin only
				buckets.PUT("/:name/policy", middleware.AdminMiddleware(), bucketHandler.SetBucketPolicy) // Admin only
				buckets.GET("/:name/policy", bucketHandler.GetBucketPolicy)

				// Object routes within a bucket - use :name to match the bucket parameter above
				buckets.GET("/:name/objects", bucketHandler.ListObjects)
				buckets.POST("/:name/objects", bucketHandler.UploadObject)
				buckets.POST("/:name/objects/async", bucketHandler.UploadObjectAsync) // Async upload
				buckets.POST("/:name/objects/move", bucketHandler.MoveObject)         // Move object
				buckets.POST("/:name/objects/rename", bucketHandler.RenameObject)     // Rename object
				buckets.POST("/:name/folders/move", bucketHandler.MoveFolder)         // Move folder recursively
				buckets.GET("/:name/objects/*key", bucketHandler.DownloadObject)
				buckets.DELETE("/:name/objects/*key", bucketHandler.DeleteObject)
				buckets.HEAD("/:name/objects/*key", bucketHandler.HeadObject)
			}

			// Upload status routes (for async uploads)
			uploads := protected.Group("/uploads")
			{
				uploads.GET("", bucketHandler.ListUploads)
				uploads.GET("/:id/status", bucketHandler.GetUploadStatus)
			}

			// Policy routes
			policyHandler := NewPolicyHandler(cfg)
			policies := protected.Group("/policies")
			{
				policies.GET("", policyHandler.ListPolicies)                                                              // Regular users see their policies, admins see all
				policies.POST("", middleware.AdminMiddleware(), policyHandler.CreatePolicy)                               // Admin only
				policies.GET("/:id", middleware.AdminMiddleware(), policyHandler.GetPolicy)                               // Admin only
				policies.PUT("/:id", middleware.AdminMiddleware(), policyHandler.UpdatePolicy)                            // Admin only
				policies.DELETE("/:id", middleware.AdminMiddleware(), policyHandler.DeletePolicy)                         // Admin only
				policies.POST("/users/:user_id/attach", middleware.AdminMiddleware(), policyHandler.AttachPolicyToUser)   // Admin only
				policies.DELETE("/users/:user_id/detach/:policy_id", middleware.AdminMiddleware(), policyHandler.DetachPolicyFromUser) // Admin only
			}

			// S3 Configuration routes (admin only)
			s3ConfigHandler := NewS3ConfigHandler(cfg)
			s3Configs := protected.Group("/s3-configs")
			s3Configs.Use(middleware.AdminMiddleware())
			{
				s3Configs.GET("", s3ConfigHandler.ListS3Configs)
				s3Configs.POST("", s3ConfigHandler.CreateS3Config)
				s3Configs.GET("/:id", s3ConfigHandler.GetS3Config)
				s3Configs.PUT("/:id", s3ConfigHandler.UpdateS3Config)
				s3Configs.DELETE("/:id", s3ConfigHandler.DeleteS3Config)
			}
		}

		// Logout (requires authentication)
		api.POST("/auth/logout", middleware.AuthMiddleware(cfg.Auth.JWTSecret), authHandler.Logout)
	}
}
