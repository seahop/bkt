package main

import (
	"context"
	"log"
	"net/http"
	"bkt/internal/api"
	"bkt/internal/config"
	"bkt/internal/database"
	"os"
	"os/signal"
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

	// Create storage directory if it doesn't exist
	if err := os.MkdirAll(cfg.Storage.RootPath, 0755); err != nil {
		log.Fatalf("Failed to create storage directory: %v", err)
	}

	// Setup router
	router := api.SetupRouter(cfg)

	// Verify TLS is enabled
	if !cfg.TLS.Enabled {
		log.Fatal("TLS must be enabled. Set TLS_ENABLED=true")
	}

	// Create HTTPS server
	httpsAddr := cfg.Server.Host + ":9443"
	httpsServer := &http.Server{
		Addr:    httpsAddr,
		Handler: router,
	}

	// Start HTTPS server
	go func() {
		log.Printf("Starting HTTPS server on %s", httpsAddr)
		log.Printf("TLS Certificate: %s", cfg.TLS.CertFile)
		log.Printf("TLS Key: %s", cfg.TLS.KeyFile)

		if err := httpsServer.ListenAndServeTLS(cfg.TLS.CertFile, cfg.TLS.KeyFile); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start HTTPS server: %v", err)
		}
	}()

	// Wait for interrupt signal to gracefully shut down the server
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := httpsServer.Shutdown(ctx); err != nil {
		log.Printf("HTTPS server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}
