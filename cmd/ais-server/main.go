package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fvsever/jiskta-ais/internal/api"
	"github.com/fvsever/jiskta-ais/internal/auth"
	"github.com/fvsever/jiskta-ais/internal/ingest"
	"github.com/fvsever/jiskta-ais/internal/store"
)

func main() {
	// --- Configuration from environment ---
	port := getenv("PORT", "8081")
	dataDir := getenv("AIS_DATA_DIR", "/data/ais")
	supabaseURL := getenv("SUPABASE_URL", "")
	supabaseKey := getenv("SUPABASE_SERVICE_KEY", "")
	aistreamKey := getenv("AISSTREAM_API_KEY", "")

	if supabaseURL == "" || supabaseKey == "" {
		log.Fatal("SUPABASE_URL and SUPABASE_SERVICE_KEY must be set")
	}

	// --- Storage (jiskta-core via CGo) ---
	coreClient, err := store.NewCoreClient(dataDir)
	if err != nil {
		log.Fatalf("Failed to open jiskta-core store at %s: %v", dataDir, err)
	}
	defer coreClient.Close()

	// --- Auth ---
	supabaseAuth := auth.NewSupabaseAuth(supabaseURL, supabaseKey)

	// --- HTTP API server ---
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           api.NewServer(supabaseAuth, coreClient).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      120 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// --- AIS ingestor (if API key provided) ---
	var aissStream *ingest.AISStream
	if aistreamKey != "" {
		cleaner := ingest.NewCleaner()
		aissStream = ingest.NewAISStream(aistreamKey, coreClient, cleaner)
		go func() {
			if err := aissStream.Run(context.Background()); err != nil {
				log.Printf("AIS stream error: %v", err)
			}
		}()
		log.Println("AIS ingestor started")
	} else {
		log.Println("AISSTREAM_API_KEY not set — ingest disabled, API-only mode")
	}

	// --- Start HTTP server ---
	go func() {
		log.Printf("jiskta-ais listening on :%s", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// --- Graceful shutdown ---
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("Shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("HTTP shutdown error: %v", err)
	}
	if aissStream != nil {
		aissStream.Stop()
	}
	log.Println("Stopped.")
}

func getenv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}
