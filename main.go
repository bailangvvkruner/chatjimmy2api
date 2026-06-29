package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	defaultPort      = "28094"
	defaultAPIKey    = "[REDACTED:sk-secret]"
	defaultUpstream  = "https://chatjimmy.ai/api/chat"
)

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	// ── Configuration (env-based, Docker friendly) ──
	addr := ":" + getEnv("PORT", defaultPort)
	if a := os.Getenv("LISTEN_ADDR"); a != "" {
		addr = a
	}

	upstreamURL := getEnv("UPSTREAM_URL", defaultUpstream)
	apiKey := getEnv("API_KEY", defaultAPIKey)

	// ── Initialize dependencies ──
	client := NewUpstreamClient(upstreamURL)
	server := NewServer(client, apiKey)

	// ── HTTP server ──
	srv := &http.Server{
		Addr:         addr,
		Handler:      server,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 0, // streaming requires no write timeout
		IdleTimeout:  120 * time.Second,
	}

	// ── Graceful shutdown ──
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		<-ctx.Done()
		log.Println("shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown error: %v", err)
		}
	}()

	// ── Start ──
	log.Printf("chatjimmy2api starting on %s", addr)
	log.Printf("upstream: %s", upstreamURL)
	if apiKey != "" {
		log.Println("auth: enabled (Bearer token)")
	} else {
		log.Println("auth: disabled")
	}
	log.Print("endpoints:")
	log.Print("  GET  /health")
	log.Print("  GET  /v1/models")
	log.Print("  POST /v1/chat/completions (stream=true/false)")

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}

	log.Println("server stopped")
}
