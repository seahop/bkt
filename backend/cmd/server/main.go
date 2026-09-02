package main

import (
	"context"
	"log"
	"net/http"
	"bkt/internal/api"
	"bkt/internal/config"
	"bkt/internal/database"
	"bkt/internal/metrics"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	// Load configuration
	cfg := config.Load()
	log.Println("Configuration loaded")

	// Wait for database to be ready
	log.Println("Waiting for database to be ready...")
	time.Sleep(3 * time.Second)

	// Initialize database
	if err := database.Initialize(cfg); err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}

	// Initialize default admin user
	if err := database.InitializeDefaultAdmin(cfg); err != nil {
		log.Fatalf("Failed to initialize default admin: %v", err)
	}

	// Auto-provision any S3 buckets declared in .env (S3_BUCKETS)
	if err := database.InitializeStartupBuckets(cfg); err != nil {
		log.Fatalf("Failed to provision startup S3 buckets: %v", err)
	}

	// Create storage directory if it doesn't exist
	if err := os.MkdirAll(cfg.Storage.RootPath, 0750); err != nil {
		log.Fatalf("Failed to create storage directory: %v", err)
	}

	// Start Prometheus storage metrics collector (runs every 60s)
	metrics.StartStorageMetricsCollector()

	// Periodically clean up expired revoked tokens and abandoned multipart uploads (every 15 minutes)
	go func() {
		ticker := time.NewTicker(15 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			if err := database.CleanupExpiredTokens(); err != nil {
				log.Printf("Failed to clean up expired tokens: %v", err)
			}
			if err := database.CleanupAbandonedMultipartUploads(cfg); err != nil {
				log.Printf("Failed to clean up abandoned multipart uploads: %v", err)
			}
			if err := database.CleanupOldAuditLogs(auditRetentionDays()); err != nil {
				log.Printf("Failed to prune old audit logs: %v", err)
			}
			if err := database.CleanupExpiredTemporaryKeys(); err != nil {
				log.Printf("Failed to prune expired temporary keys: %v", err)
			}
		}
	}()

	// Mirror replicating buckets every 5 minutes.
	go func() {
		time.Sleep(45 * time.Second)
		api.RunReplicationSweep(cfg)
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			api.RunReplicationSweep(cfg)
		}
	}()

	// Apply bucket lifecycle rules hourly (age-based expiry of current
	// objects and noncurrent versions), with one pass shortly after startup
	// so restarts don't defer overdue expirations by up to an hour.
	go func() {
		time.Sleep(30 * time.Second)
		api.RunLifecycleSweep(cfg)
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			api.RunLifecycleSweep(cfg)
		}
	}()

	// Two listeners on the same process:
	//   - console: web UI (embedded) + REST API, browser-facing
	//   - s3:      S3-compatible API at root paths, for aws-cli / s3fs
	// S3 clients address buckets at the host root, so the S3 API cannot share a
	// listener with the UI — hence the split ports.
	consoleRouter := api.SetupConsoleRouter(cfg)
	s3Router := api.SetupS3Router(cfg)

	consoleAddr := cfg.Server.Host + ":" + cfg.Server.ConsolePort
	s3Addr := cfg.Server.Host + ":" + cfg.Server.S3APIPort

	if cfg.TLS.Enabled {
		log.Printf("TLS enabled (cert=%s key=%s)", cfg.TLS.CertFile, cfg.TLS.KeyFile)
	} else {
		// Production config validation already rejects TLS_ENABLED=false, so this
		// only happens in dev / behind a TLS-terminating proxy.
		log.Println("TLS disabled — serving plain HTTP")
	}

	consoleServer := startServer("console", consoleAddr, consoleRouter, cfg)
	s3Server := startServer("s3-api", s3Addr, s3Router, cfg)

	// Wait for interrupt signal to gracefully shut down both servers
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down servers...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := consoleServer.Shutdown(ctx); err != nil {
		log.Printf("Console server forced to shutdown: %v", err)
	}
	if err := s3Server.Shutdown(ctx); err != nil {
		log.Printf("S3 server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}

// auditRetentionDays returns the audit-log retention window in days
// (AUDIT_RETENTION_DAYS, default 90; 0 disables pruning).
func auditRetentionDays() int {
	if v := os.Getenv("AUDIT_RETENTION_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 90
}

// startServer launches an http.Server in a goroutine, using TLS when enabled.
func startServer(name, addr string, handler http.Handler, cfg *config.Config) *http.Server {
	srv := &http.Server{
		Addr:    addr,
		Handler: handler,
		// Slowloris protection. ReadTimeout/WriteTimeout are intentionally left
		// unset: this server streams arbitrarily large object bodies in both
		// directions, and a fixed whole-request deadline would abort legitimate
		// large transfers. ReadHeaderTimeout + IdleTimeout bound the cheap-to-
		// abuse phases without capping transfer duration.
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MiB
	}
	go func() {
		if cfg.TLS.Enabled {
			log.Printf("Starting %s server (HTTPS) on %s", name, addr)
			if err := srv.ListenAndServeTLS(cfg.TLS.CertFile, cfg.TLS.KeyFile); err != nil && err != http.ErrServerClosed {
				log.Fatalf("Failed to start %s server: %v", name, err)
			}
		} else {
			log.Printf("Starting %s server (HTTP) on %s", name, addr)
			if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("Failed to start %s server: %v", name, err)
			}
		}
	}()
	return srv
}
