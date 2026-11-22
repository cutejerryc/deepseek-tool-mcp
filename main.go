package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/cutejerryc/deepseek-tool-mcp/internal/deepseek"
	"github.com/cutejerryc/deepseek-tool-mcp/internal/mcpserver"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("deepseek-search-mcp %s\n", version)
		return
	}

	logger := log.New(os.Stderr, "deepseek-search-mcp: ", log.LstdFlags|log.Lmsgprefix)

	apiKey := strings.TrimSpace(os.Getenv("DEEPSEEK_API_KEY"))
	if apiKey == "" {
		logger.Fatal("DEEPSEEK_API_KEY is not set; set it as a user environment variable and restart ZCode")
	}

	baseURL := envOr("DEEPSEEK_BASE_URL", deepseek.DefaultBaseURL)
	timeout := envDuration(logger, "DEEPSEEK_TIMEOUT_SECONDS", 120*time.Second)
	maxAttempts := envInt(logger, "DEEPSEEK_MAX_ATTEMPTS", 3)

	client, err := deepseek.New(deepseek.Options{
		APIKey:      apiKey,
		BaseURL:     baseURL,
		Timeout:     timeout,
		MaxAttempts: maxAttempts,
		UserAgent:   "deepseek-search-mcp/" + version,
	})
	if err != nil {
		logger.Fatalf("invalid configuration: %v", err)
	}

	server := mcpserver.New(client, version, logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	logger.Printf("starting version=%s base_url=%s timeout=%s max_attempts=%d", version, baseURL, timeout, maxAttempts)
	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil && ctx.Err() == nil {
		logger.Fatalf("server stopped: %v", err)
	}
	logger.Print("stopped")
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envDuration(logger *log.Logger, key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		logger.Printf("ignoring invalid %s=%q; using %s", key, raw, fallback)
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

func envInt(logger *log.Logger, key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		logger.Printf("ignoring invalid %s=%q; using %d", key, raw, fallback)
		return fallback
	}
	return v
}
