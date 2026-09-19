package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"encyclopedia-ai/internal/ai"
	"encyclopedia-ai/internal/handlers"
)

func main() {
	// Provider configuration is resolved once, so a misconfigured deployment
	// fails here rather than on a user's first request.
	agent, err := ai.NewFromEnv()
	if err != nil {
		log.Fatalf("Could not configure the AI provider: %v", err)
	}
	log.Printf("AI provider ready: %s", agent.Describe())

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir("./web/static")))
	mux.HandleFunc("/api/start", handlers.New(agent).StartArticle)

	address := os.Getenv("ENCYCLOPEDIA_ADDR")
	if address == "" {
		address = ":8080"
	}

	server := &http.Server{
		Addr:              address,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-shutdown
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("Server shutdown failed: %v", err)
		}
	}()

	log.Printf("Server starting on %s", address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Could not start server: %s", err)
	}
}
