package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"encyclopedia-ai/internal/jobs"
	"encyclopedia-ai/internal/llm"
	"encyclopedia-ai/internal/orchestrator"
)

const passingEvaluation = `{"scores":{"factual_accuracy":9,"completeness":9,"neutrality":9,"clarity":9,"structure":9},"overall":9,"critical_issues":[]}`

// testAgent produces an article without a model.
type testAgent struct {
	generateErr error
	release     chan struct{}
}

func (a *testAgent) GenerateArticle(ctx context.Context, _ string, sink llm.Sink) (string, error) {
	if a.generateErr != nil {
		return "", a.generateErr
	}
	if a.release != nil {
		select {
		case <-a.release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	sink.Emit(llm.Delta{Reasoning: "deciding the scope"})
	sink.Emit(llm.Delta{Content: "the article"})
	return "the article", nil
}

func (a *testAgent) EvaluateArticle(context.Context, string, llm.Sink) (string, error) {
	return passingEvaluation, nil
}
func (a *testAgent) PlanRevision(context.Context, string, string, llm.Sink) (string, error) {
	return `{"instructions":["improve"]}`, nil
}
func (a *testAgent) ReviseArticle(context.Context, string, string, string, llm.Sink) (string, error) {
	return "revised", nil
}
func (a *testAgent) References(context.Context, string, llm.Sink) (string, error) {
	return `{"references":[]}`, nil
}
func (a *testAgent) Infobox(context.Context, string, string, llm.Sink) (string, error) {
	return `{"rows":[]}`, nil
}
func (a *testAgent) SeeAlso(context.Context, string, llm.Sink) (string, error) {
	return `{"topics":[]}`, nil
}
func (a *testAgent) CategorizeArticle(context.Context, string, llm.Sink) (string, error) {
	return `{"categories":[]}`, nil
}

// newServer starts the full API against a real runner.
func newServer(t *testing.T, agent orchestrator.Agent) (*httptest.Server, *jobs.Runner) {
	t.Helper()

	store := jobs.NewMemoryStore()
	runner := jobs.NewRunner(store, agent, jobs.NewBroker(), 1)
	runner.FlushInterval = time.Millisecond
	runner.FlushBytes = 8

	ctx, cancel := context.WithCancel(context.Background())
	runner.Start(ctx)

	mux := http.NewServeMux()
	New(runner).Routes(mux)
	server := httptest.NewServer(mux)

	t.Cleanup(func() {
		server.Close()
		cancel()
		runner.Wait()
	})
	return server, runner
}

func createJob(t *testing.T, server *httptest.Server, body string) jobs.Job {
	t.Helper()
	response, err := http.Post(server.URL+"/api/articles", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST returned error: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusAccepted {
		payload, _ := io.ReadAll(response.Body)
		t.Fatalf("status = %d, want 202: %s", response.StatusCode, payload)
	}

	var job jobs.Job
	if err := json.NewDecoder(response.Body).Decode(&job); err != nil {
		t.Fatalf("decode job: %v", err)
	}
	return job
}

func awaitTerminal(t *testing.T, server *httptest.Server, id string) jobs.Job {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := http.Get(server.URL + "/api/articles/" + id)
		if err != nil {
			t.Fatalf("GET returned error: %v", err)
		}
		var job jobs.Job
		decodeErr := json.NewDecoder(response.Body).Decode(&job)
		response.Body.Close()
		if decodeErr != nil {
			t.Fatalf("decode job: %v", decodeErr)
		}
		if job.Status.Terminal() {
			return job
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("job never finished")
	return jobs.Job{}
}

// sseEvent is one framed event read back from the stream.
type sseEvent struct {
	ID   string
	Type string
	Data string
}

// readSSE reads framed events until the stream closes.
func readSSE(t *testing.T, body io.Reader) []sseEvent {
	t.Helper()

	var events []sseEvent
	var current sseEvent
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if current.Type != "" {
				events = append(events, current)
			}
			current = sseEvent{}
		case strings.HasPrefix(line, "id: "):
			current.ID = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			current.Type = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			current.Data = strings.TrimPrefix(line, "data: ")
		}
	}
	return events
}

// rebuild reassembles a stream's text from token or reasoning events.
func rebuild(t *testing.T, events []sseEvent, eventType, stream string) string {
	t.Helper()
	var out strings.Builder
	for _, e := range events {
		if e.Type != eventType {
			continue
		}
		var payload jobs.TokenPayload
		if err := json.Unmarshal([]byte(e.Data), &payload); err != nil {
			t.Fatalf("decode %s payload: %v", eventType, err)
		}
		if payload.Stream == stream {
			out.WriteString(payload.Text)
		}
	}
	return out.String()
}

// The request must return immediately; generation happens afterwards.
func TestCreateArticleReturnsBeforeGenerating(t *testing.T) {
	server, _ := newServer(t, &testAgent{})

	job := createJob(t, server, `{"topic":"Bacon","max_rounds":1}`)
	if job.ID == "" || job.Status != jobs.StatusQueued {
		t.Fatalf("job = %+v, want a queued job with an id", job)
	}

	finished := awaitTerminal(t, server, job.ID)
	if finished.Status != jobs.StatusComplete {
		t.Fatalf("status = %q, error = %q", finished.Status, finished.Error)
	}
	if finished.State == nil || finished.State.CurrentArticle != "the article" {
		t.Fatalf("state = %+v", finished.State)
	}
}

func TestStreamDeliversTheWholeJob(t *testing.T) {
	server, _ := newServer(t, &testAgent{})
	job := createJob(t, server, `{"topic":"Bacon","max_rounds":1}`)

	response, err := http.Get(server.URL + "/api/articles/" + job.ID + "/events")
	if err != nil {
		t.Fatalf("GET events returned error: %v", err)
	}
	defer response.Body.Close()

	if contentType := response.Header.Get("Content-Type"); contentType != "text/event-stream" {
		t.Fatalf("content type = %q", contentType)
	}

	events := readSSE(t, response.Body)
	if rebuilt := rebuild(t, events, jobs.EventToken, jobs.StreamArticle); rebuilt != "the article" {
		t.Errorf("article rebuilt from the stream = %q", rebuilt)
	}
	// The reasoning trace travels on its own event and never joins the article.
	if rebuilt := rebuild(t, events, jobs.EventReasoning, jobs.StreamArticle); rebuilt != "deciding the scope" {
		t.Errorf("reasoning rebuilt from the stream = %q", rebuilt)
	}

	last := events[len(events)-1]
	if last.Type != "closed" {
		t.Fatalf("stream ended with %q, want an explicit close", last.Type)
	}
	if !strings.Contains(last.Data, string(jobs.StatusComplete)) {
		t.Fatalf("close event = %q", last.Data)
	}
}

// Resuming is the point of the log: a client that drops mid-job must be able
// to pick up exactly where it stopped, with no gap and no repetition.
func TestStreamResumesFromTheLastSeenEvent(t *testing.T) {
	server, _ := newServer(t, &testAgent{})
	job := createJob(t, server, `{"topic":"Bacon","max_rounds":1}`)
	awaitTerminal(t, server, job.ID)

	full, err := http.Get(server.URL + "/api/articles/" + job.ID + "/events")
	if err != nil {
		t.Fatalf("GET events returned error: %v", err)
	}
	everything := readSSE(t, full.Body)
	full.Body.Close()

	if len(everything) < 4 {
		t.Fatalf("only %d events to resume through", len(everything))
	}
	cutoff := everything[2].ID

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/api/articles/"+job.ID+"/events", nil)
	request.Header.Set("Last-Event-ID", cutoff)
	resumed, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("resume returned error: %v", err)
	}
	tail := readSSE(t, resumed.Body)
	resumed.Body.Close()

	// Everything after the cutoff, and nothing at or before it.
	want := everything[3:]
	if len(tail) != len(want) {
		t.Fatalf("resumed %d events, want %d", len(tail), len(want))
	}
	for i := range want {
		if tail[i].ID != want[i].ID || tail[i].Data != want[i].Data {
			t.Fatalf("event %d differs: got %+v want %+v", i, tail[i], want[i])
		}
	}

	// The same resume via the query parameter.
	viaQuery, err := http.Get(server.URL + "/api/articles/" + job.ID + "/events?from=" + cutoff)
	if err != nil {
		t.Fatalf("resume by query returned error: %v", err)
	}
	queryTail := readSSE(t, viaQuery.Body)
	viaQuery.Body.Close()
	if len(queryTail) != len(want) {
		t.Fatalf("from= resumed %d events, want %d", len(queryTail), len(want))
	}
}

// A client that arrives after the job finished still gets the whole log.
func TestStreamReplaysAFinishedJob(t *testing.T) {
	server, _ := newServer(t, &testAgent{})
	job := createJob(t, server, `{"topic":"Bacon","max_rounds":1}`)
	awaitTerminal(t, server, job.ID)

	response, err := http.Get(server.URL + "/api/articles/" + job.ID + "/events")
	if err != nil {
		t.Fatalf("GET events returned error: %v", err)
	}
	defer response.Body.Close()

	events := readSSE(t, response.Body)
	if rebuilt := rebuild(t, events, jobs.EventToken, jobs.StreamArticle); rebuilt != "the article" {
		t.Fatalf("replayed article = %q", rebuilt)
	}
}

func TestCancelStopsAJob(t *testing.T) {
	agent := &testAgent{release: make(chan struct{})}
	server, _ := newServer(t, agent)
	job := createJob(t, server, `{"topic":"Bacon","max_rounds":1}`)

	request, _ := http.NewRequest(http.MethodPost, server.URL+"/api/articles/"+job.ID+"/cancel", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("cancel returned error: %v", err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("cancel status = %d, want 204", response.StatusCode)
	}

	finished := awaitTerminal(t, server, job.ID)
	if finished.Status != jobs.StatusCanceled {
		t.Fatalf("status = %q, want canceled", finished.Status)
	}
	close(agent.release)
}

func TestFailureIsReportedOnTheJobAndTheStream(t *testing.T) {
	server, _ := newServer(t, &testAgent{generateErr: errors.New("provider unavailable")})
	job := createJob(t, server, `{"topic":"Bacon","max_rounds":1}`)

	finished := awaitTerminal(t, server, job.ID)
	if finished.Status != jobs.StatusFailed || !strings.Contains(finished.Error, "provider unavailable") {
		t.Fatalf("job = %+v", finished)
	}

	response, err := http.Get(server.URL + "/api/articles/" + job.ID + "/events")
	if err != nil {
		t.Fatalf("GET events returned error: %v", err)
	}
	defer response.Body.Close()

	events := readSSE(t, response.Body)
	var sawError bool
	for _, e := range events {
		if e.Type == jobs.EventError && strings.Contains(e.Data, "provider unavailable") {
			sawError = true
		}
	}
	if !sawError {
		t.Fatalf("the failure was not streamed: %+v", events)
	}
}

func TestRequestValidation(t *testing.T) {
	server, _ := newServer(t, &testAgent{})

	cases := []struct {
		name   string
		method string
		path   string
		body   string
		want   int
	}{
		{"empty topic", http.MethodPost, "/api/articles", `{"topic":"  "}`, http.StatusBadRequest},
		{"too many rounds", http.MethodPost, "/api/articles", `{"topic":"Bacon","max_rounds":11}`, http.StatusBadRequest},
		{"unknown field", http.MethodPost, "/api/articles", `{"topic":"Bacon","nope":1}`, http.StatusBadRequest},
		{"malformed body", http.MethodPost, "/api/articles", `{`, http.StatusBadRequest},
		{"wrong method", http.MethodGet, "/api/articles", "", http.StatusMethodNotAllowed},
		{"unknown job", http.MethodGet, "/api/articles/" + strings.Repeat("a", 32), "", http.StatusNotFound},
		{"malformed id", http.MethodGet, "/api/articles/not-an-id", "", http.StatusNotFound},
		{"path traversal", http.MethodGet, "/api/articles/..%2f..%2fetc", "", http.StatusNotFound},
		{"unknown job events", http.MethodGet, "/api/articles/" + strings.Repeat("b", 32) + "/events", "", http.StatusNotFound},
		{"cancel unknown job", http.MethodPost, "/api/articles/" + strings.Repeat("c", 32) + "/cancel", "", http.StatusNotFound},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request, err := http.NewRequest(testCase.method, server.URL+testCase.path, strings.NewReader(testCase.body))
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatalf("request returned error: %v", err)
			}
			defer response.Body.Close()
			if response.StatusCode != testCase.want {
				body, _ := io.ReadAll(response.Body)
				t.Fatalf("status = %d, want %d: %s", response.StatusCode, testCase.want, body)
			}
		})
	}
}

// The created job must be reachable at the Location it advertises.
func TestCreateAdvertisesTheJobLocation(t *testing.T) {
	server, _ := newServer(t, &testAgent{})

	response, err := http.Post(server.URL+"/api/articles", "application/json", strings.NewReader(`{"topic":"Bacon","max_rounds":1}`))
	if err != nil {
		t.Fatalf("POST returned error: %v", err)
	}
	defer response.Body.Close()

	location := response.Header.Get("Location")
	if location == "" {
		t.Fatal("no Location header")
	}
	follow, err := http.Get(server.URL + location)
	if err != nil {
		t.Fatalf("following Location returned error: %v", err)
	}
	defer follow.Body.Close()
	if follow.StatusCode != http.StatusOK {
		t.Fatalf("following Location gave %d", follow.StatusCode)
	}
}

func TestHandlerWithoutARunner(t *testing.T) {
	mux := http.NewServeMux()
	New(nil).Routes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	response, err := http.Post(server.URL+"/api/articles", "application/json", strings.NewReader(`{"topic":"Bacon"}`))
	if err != nil {
		t.Fatalf("POST returned error: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", response.StatusCode)
	}
}

func TestResumeFromParsesBothSources(t *testing.T) {
	cases := []struct {
		header string
		query  string
		want   int64
	}{
		{"", "", 0},
		{"12", "", 12},
		{"", "7", 7},
		{"12", "7", 12}, // the reconnect header wins
		{"garbage", "", 0},
		{"-5", "", 0},
	}
	for _, testCase := range cases {
		request := httptest.NewRequest(http.MethodGet, "/api/articles/x/events?from="+testCase.query, nil)
		if testCase.header != "" {
			request.Header.Set("Last-Event-ID", testCase.header)
		}
		if got := resumeFrom(request); got != testCase.want {
			t.Errorf("resumeFrom(header=%q query=%q) = %d, want %d",
				testCase.header, testCase.query, got, testCase.want)
		}
	}
}
