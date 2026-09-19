// Package handlers exposes the job API over HTTP.
//
// Generation is not performed inside a request. A POST enqueues a job and
// returns its identifier; clients follow progress on a separate SSE stream
// that can be resumed from any point in the job's event log.
package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"encyclopedia-ai/internal/jobs"
)

const (
	maxRequestBodyBytes = 16 * 1024
	defaultMaxRounds    = 3
	maxAllowedRounds    = 10

	// eventPageSize bounds one read from the store, so a client resuming a
	// long job streams it in pieces rather than buffering all of it.
	eventPageSize = 256

	// heartbeatInterval keeps idle connections alive through proxies that
	// close quiet streams. A reasoning phase can be silent for minutes.
	heartbeatInterval = 15 * time.Second
)

// Handler serves the job API.
type Handler struct {
	Runner *jobs.Runner
	Store  jobs.Store
	Broker *jobs.Broker
}

// New builds a handler over a runner.
func New(runner *jobs.Runner) *Handler {
	if runner == nil {
		return &Handler{}
	}
	return &Handler{Runner: runner, Store: runner.Store, Broker: runner.Broker}
}

// Routes registers the API on a mux.
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/articles", h.CreateArticle)
	mux.HandleFunc("GET /api/articles/{id}", h.GetArticle)
	mux.HandleFunc("GET /api/articles/{id}/events", h.StreamArticle)
	mux.HandleFunc("POST /api/articles/{id}/cancel", h.CancelArticle)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		log.Printf("[http] write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// CreateArticle enqueues a job and returns immediately. Generation takes
// minutes, so the request does not wait for it.
func (h *Handler) CreateArticle(w http.ResponseWriter, r *http.Request) {
	if h.Runner == nil {
		writeError(w, http.StatusServiceUnavailable, "Generation is not configured")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
	var request struct {
		Topic     string `json:"topic"`
		MaxRounds int    `json:"max_rounds"`
	}
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if request.MaxRounds <= 0 {
		request.MaxRounds = defaultMaxRounds
	}
	if request.MaxRounds > maxAllowedRounds {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("max_rounds cannot exceed %d", maxAllowedRounds))
		return
	}

	job, err := h.Runner.Enqueue(r.Context(), request.Topic, request.MaxRounds)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	w.Header().Set("Location", "/api/articles/"+job.ID)
	writeJSON(w, http.StatusAccepted, job)
}

// GetArticle returns a job and, once it has finished, its article state.
// A client that reconnects after completion can use this alone and skip the
// event log entirely.
func (h *Handler) GetArticle(w http.ResponseWriter, r *http.Request) {
	job, ok := h.lookup(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, job)
}

// CancelArticle stops a queued or running job.
func (h *Handler) CancelArticle(w http.ResponseWriter, r *http.Request) {
	if h.Runner == nil {
		writeError(w, http.StatusServiceUnavailable, "Generation is not configured")
		return
	}
	id := r.PathValue("id")
	if !jobs.ValidID(id) {
		writeError(w, http.StatusNotFound, "No such article")
		return
	}

	if err := h.Runner.Cancel(r.Context(), id); err != nil {
		if errors.Is(err, jobs.ErrNotFound) {
			writeError(w, http.StatusNotFound, "No such article")
			return
		}
		writeError(w, http.StatusInternalServerError, "Could not cancel the job")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) lookup(w http.ResponseWriter, r *http.Request) (jobs.Job, bool) {
	if h.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "Generation is not configured")
		return jobs.Job{}, false
	}
	id := r.PathValue("id")
	// Validate before touching the store, so a hostile identifier never
	// reaches a storage backend.
	if !jobs.ValidID(id) {
		writeError(w, http.StatusNotFound, "No such article")
		return jobs.Job{}, false
	}

	job, err := h.Store.Job(r.Context(), id)
	if err != nil {
		if errors.Is(err, jobs.ErrNotFound) {
			writeError(w, http.StatusNotFound, "No such article")
			return jobs.Job{}, false
		}
		writeError(w, http.StatusInternalServerError, "Could not read the job")
		return jobs.Job{}, false
	}
	return job, true
}

// resumeFrom reads the client's position in the log, from the standard
// Last-Event-ID reconnect header or an explicit from parameter.
func resumeFrom(r *http.Request) int64 {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("from")
	}
	cursor, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || cursor < 0 {
		return 0
	}
	return cursor
}

// StreamArticle streams a job's event log as SSE, starting after the client's
// last seen sequence number and continuing until the job finishes.
func (h *Handler) StreamArticle(w http.ResponseWriter, r *http.Request) {
	job, ok := h.lookup(w, r)
	if !ok {
		return
	}
	flusher, isFlusher := w.(http.Flusher)
	if !isFlusher {
		writeError(w, http.StatusInternalServerError, "Streaming not supported")
		return
	}

	// Subscribe before the first read, so an event appended between the read
	// and the subscription still wakes this stream.
	var signal <-chan struct{}
	if h.Broker != nil {
		var release func()
		signal, release = h.Broker.Subscribe(job.ID)
		defer release()
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Proxies that buffer responses would defeat streaming entirely.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ctx := r.Context()
	cursor := resumeFrom(r)
	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		// Drain everything available before deciding whether the job is over,
		// so the final events are never dropped by a race with completion.
		for {
			events, err := h.Store.Events(ctx, job.ID, cursor, eventPageSize)
			if err != nil {
				if !errors.Is(err, jobs.ErrNotFound) {
					log.Printf("[http] reading events for %s: %v", job.ID, err)
				}
				return
			}
			if len(events) == 0 {
				break
			}
			for _, entry := range events {
				if err := writeEvent(w, entry); err != nil {
					return
				}
				cursor = entry.Seq
			}
			flusher.Flush()
		}

		current, err := h.Store.Job(ctx, job.ID)
		if err != nil {
			return
		}
		if current.Status.Terminal() {
			// The client may have joined after the work finished, so say so
			// explicitly rather than leaving the stream to time out.
			if err := writeNamed(w, "closed", current.Status); err == nil {
				flusher.Flush()
			}
			return
		}

		select {
		case <-ctx.Done():
			return
		case <-signal:
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeEvent frames one log entry. The id line is what lets a reconnecting
// client tell the server where it left off.
func writeEvent(w http.ResponseWriter, entry jobs.Event) error {
	_, err := fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", entry.Seq, entry.Type, entry.Data)
	return err
}

func writeNamed(w http.ResponseWriter, name string, payload any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, encoded)
	return err
}
