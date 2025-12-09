package api

import (
	authpkg "bkt/internal/auth"
	"bkt/internal/config"
	"bkt/internal/middleware"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func SetupRouter(cfg *config.Config) *gin.Engine {
	router := gin.Default()

	// CORS configuration
	router.Use(cors.New(cors.Config{
		AllowOrigins: []string{
			"https://localhost",
			"https://localhost:443",
			"https://localhost:5173",
			"http://localhost:5173",
			"http://localhost:3000",
		},
		AllowMethods:     []string{"GET", "POST", "PUT", "DELETE", "OPTIONS", "HEAD"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization", "X-Amz-Date", "X-Amz-Content-Sha256"},
		ExposeHeaders:    []string{"Content-Length", "ETag", "X-Amz-Request-Id"},
		AllowCredentials: true,
	}))

	// Health check endpoint
	router.GET("/health", func(c *gin.Context) {
		c.JSON(200, gin.H{"status": "healthy"})
	})

	// API routes group
	api := router.Group("/api")
	{
		// Auth routes (no authentication required)
		authHandler := NewAuthHandler(cfg)
		auth := api.Group("/auth")
		{
			auth.POST("/register", authHandler.Register)
			auth.POST("/login", authHandler.Login)
			auth.POST("/refresh", authHandler.RefreshToken)

			// SSO configuration endpoint
			ssoConfigHandler := NewSSOConfigHandler(cfg)
			auth.GET("/sso/config", ssoConfigHandler.GetSSOConfig)

			// Google OAuth routes
			googleHandler := authpkg.NewGoogleOAuthHandler(cfg)
			auth.GET("/google/login", googleHandler.InitiateGoogleLogin)
			auth.GET("/google/callback", googleHandler.HandleGoogleCallback)

			// Vault JWT routes
			vaultHandler := authpkg.NewVaultJWTHandler(cfg)
			auth.POST("/vault/login", vaultHandler.LoginWithVaultJWT)
		}

		// Protected routes (require authentication)
		protected := api.Group("")
		protected.Use(middleware.AuthMiddleware(cfg.Auth.JWTSecret))
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
				buckets.DELETE("/:name", middleware.AdminMiddleware(), bucketHandler.DeleteBucket) // Admin only
				buckets.PUT("/:name/policy", middleware.AdminMiddleware(), bucketHandler.SetBucketPolicy) // Admin only
				buckets.GET("/:name/policy", bucketHandler.GetBucketPolicy)

				// Object routes within a bucket - use :name to match the bucket parameter above
				buckets.GET("/:name/objects", bucketHandler.ListObjects)
				buckets.POST("/:name/objects", bucketHandler.UploadObject)
				buckets.GET("/:name/objects/*key", bucketHandler.DownloadObject)
				buckets.DELETE("/:name/objects/*key", bucketHandler.DeleteObject)
				buckets.HEAD("/:name/objects/*key", bucketHandler.HeadObject)
			}

			// Policy routes
			policyHandler := NewPolicyHandler(cfg)
			policies := protected.Group("/policies")
			{
				policies.GET("", policyHandler.ListPolicies) // Regular users see their policies, admins see all
				policies.POST("", middleware.AdminMiddleware(), policyHandler.CreatePolicy) // Admin only
				policies.GET("/:id", middleware.AdminMiddleware(), policyHandler.GetPolicy) // Admin only
				policies.PUT("/:id", middleware.AdminMiddleware(), policyHandler.UpdatePolicy) // Admin only
				policies.DELETE("/:id", middleware.AdminMiddleware(), policyHandler.DeletePolicy) // Admin only
				policies.POST("/users/:user_id/attach", middleware.AdminMiddleware(), policyHandler.AttachPolicyToUser) // Admin only
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

	// S3-compatible API routes (authenticated with AWS Signature V4)
	// These routes enable s3fs-fuse and other S3 clients to mount buckets
	s3Handler := NewS3APIHandler(cfg)
	s3 := router.Group("")
	s3.Use(middleware.S3AuthMiddleware())
	{
		// Service-level operations
		s3.GET("/", s3Handler.ListBuckets)

		// Bucket-level operations
		s3.HEAD("/:bucket", s3Handler.HeadBucket)
		s3.GET("/:bucket", s3Handler.ListObjects)
		s3.PUT("/:bucket", s3Handler.CreateBucket) // Currently disabled

		// Object-level operations
		s3.HEAD("/:bucket/*key", s3Handler.HeadObject)
		s3.GET("/:bucket/*key", s3Handler.GetObject)
		s3.PUT("/:bucket/*key", s3Handler.PutObject)
		s3.DELETE("/:bucket/*key", s3Handler.DeleteObject)
	}

	return router
}
