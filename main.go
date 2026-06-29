package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

const (
	defaultPort      = "28094"
	defaultAPIKey    = "[REDACTED:sk-secret]"
	defaultUpstream  = "https://chatjimmy.ai/api/chat"
)

var logLevel int

const (
	levelError = iota // 0 - only errors
	levelWarn         // 1 - errors + warnings
	levelInfo         // 2 - + info (default)
	levelDebug        // 3 - + debug
)

func init() {
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "error":
		logLevel = levelError
	case "warn", "warning":
		logLevel = levelWarn
	case "debug":
		logLevel = levelDebug
	default:
		logLevel = levelInfo
	}

	// Capture all log output into the ring buffer (both stderr + buffer)
	log.SetOutput(io.MultiWriter(os.Stderr, debugLog))
}

func logDebug(format string, args ...any) {
	if logLevel >= levelDebug {
		log.Printf("[DEBUG] "+format, args...)
	}
}

func logInfo(format string, args ...any) {
	if logLevel >= levelInfo {
		log.Printf("[INFO] "+format, args...)
	}
}

func logWarn(format string, args ...any) {
	if logLevel >= levelWarn {
		log.Printf("[WARN] "+format, args...)
	}
}

func logError(format string, args ...any) {
	log.Printf("[ERROR] "+format, args...)
}

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

	// ── Parse model list (comma-separated, from env or default) ──
	modelsEnv := os.Getenv("UPSTREAM_MODELS")
	var modelIDs []string
	if modelsEnv != "" {
		modelIDs = strings.Split(modelsEnv, ",")
		for i := range modelIDs {
			modelIDs[i] = strings.TrimSpace(modelIDs[i])
		}
	} else {
		modelIDs = DefaultChatJimmyModels
	}

	// ── Initialize dependencies ──
	client := NewUpstreamClient(upstreamURL)
	server := NewServer(client, apiKey, modelIDs)

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
	log.Print("  GET  /v1/admin/logs (backdoor, same auth)")

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}

	log.Println("server stopped")
}
