package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"encyclopedia-ai/internal/ai"
	"encyclopedia-ai/internal/handlers"
	"encyclopedia-ai/internal/jobs"
)

func main() {
	// Provider configuration is resolved once, so a misconfigured deployment
	// fails here rather than on a user's first request.
	agent, err := ai.NewFromEnv()
	if err != nil {
		log.Fatalf("Could not configure the AI provider: %v", err)
	}
	log.Printf("AI provider ready: %s", agent.Describe())

	// Workers run generation off the request path. Generation is GPU bound on
	// a local provider, so one worker is the default.
	workers := 1
	if raw := os.Getenv("JOB_WORKERS"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed <= 0 {
			log.Fatalf("JOB_WORKERS must be a positive integer, got %q", raw)
		}
		workers = parsed
	}

	store := jobs.NewMemoryStore()
	runner := jobs.NewRunner(store, agent, jobs.NewBroker(), workers)

	workerCtx, stopWorkers := context.WithCancel(context.Background())
	defer stopWorkers()
	runner.Start(workerCtx)
	log.Printf("Job runner started with %d worker(s)", workers)

	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir("./web/static")))
	handlers.New(runner).Routes(mux)

	address := os.Getenv("ENCYCLOPEDIA_ADDR")
	if address == "" {
		address = ":8080"
	}

	server := &http.Server{
		Addr:              address,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No write timeout: an event stream is held open for the life of a
		// job, which can run for tens of minutes.
	}

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-shutdown
		log.Printf("Shutting down")

		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("Server shutdown failed: %v", err)
		}
		// Stop the workers only once the listener is closed, so in-flight
		// jobs get a chance to record their final state.
		stopWorkers()
		runner.Wait()
	}()

	log.Printf("Server starting on %s", address)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Could not start server: %s", err)
	}
	runner.Wait()
}
