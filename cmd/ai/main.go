package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"apiservices/ai-model-gateway/internal/ai/api"
	"apiservices/ai-model-gateway/internal/ai/auth"
	"apiservices/ai-model-gateway/internal/ai/gateway"
)

func main() {
	logger := log.New(os.Stdout, "[ai] ", log.LstdFlags)

	port := envString("PORT", "30002")
	apiKey := envString("AI_API_KEY", "dev-ai-key")
	if apiKey == "dev-ai-key" {
		logger.Println("AI_API_KEY not set, using default development key")
	}

	service := gateway.NewService()
	handler := api.NewHandler(service)

	mux := http.NewServeMux()
	mux.Handle("/v1/ai/", auth.Middleware(apiKey)(handler))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})

	server := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadTimeout:       20 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Printf("service listening on :%s", port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("server failed: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Printf("shutdown error: %v", err)
	}
}

func envString(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}
