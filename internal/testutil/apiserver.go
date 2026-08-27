// Package testutil holds test helpers shared between the API facade contract tests and the
// Terraform provider tests.
package testutil

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Request is one recorded call, captured so a test can assert on the exact wire shape the facade
// produced: the path it addressed, the credential it presented, and the body it sent.
type Request struct {
	Method        string
	Path          string
	RawPath       string
	Query         string
	Authorization string
	UserAgent     string
	ContentType   string
	Body          string
}

// DecodeBody unmarshals the recorded request body into v.
func (r Request) DecodeBody(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal([]byte(r.Body), v); err != nil {
		t.Fatalf("decode request body %q: %v", r.Body, err)
	}
}

// Response is a canned reply.
type Response struct {
	Status int
	// Body is written verbatim, so a test can supply a malformed or non-JSON body on purpose.
	Body string
	// Header holds extra response headers, for example Retry-After or a request ID.
	Header map[string]string
}

// JSON is a 200 response with a JSON body.
func JSON(body string) Response { return Response{Status: http.StatusOK, Body: body} }

// Status is a response with no body.
func Status(status int) Response { return Response{Status: status} }

// Error is a response carrying the Cloud API's `{code, message}` error shape.
func Error(status int, message string) Response {
	body, err := json.Marshal(map[string]any{"code": status, "message": message})
	if err != nil {
		panic(err)
	}
	return Response{Status: status, Body: string(body)}
}

// APIServer is an httptest server that answers by operation. Routes are keyed by the trailing path
// segment, which is the operation name for the Cloud API's RPC-style paths and the resource
// collection for the Admin API's REST-style ones; UseFullPath switches to matching on the whole
// path when that distinction matters.
type APIServer struct {
	*httptest.Server

	mu       sync.Mutex
	routes   map[string][]Response
	requests []Request

	// UseFullPath matches routes on the request's full path rather than its last segment.
	UseFullPath bool
	// NotFoundResponse answers an unrouted request. It defaults to a 404 carrying an explanatory
	// body, so an unexpected call fails the test loudly rather than hanging.
	NotFoundResponse *Response

	t *testing.T
}

// NewAPIServer starts a server and registers it for cleanup.
func NewAPIServer(t *testing.T) *APIServer {
	t.Helper()
	s := &APIServer{routes: map[string][]Response{}, t: t}
	s.Server = httptest.NewServer(s)
	t.Cleanup(s.Close)
	return s
}

// On queues responses for an operation. Queued responses are consumed in order, and the last one
// repeats once the queue is exhausted, so a test can script a sequence such as
// PENDING, PENDING, ACTIVE without knowing exactly how many polls will occur.
func (s *APIServer) On(operation string, responses ...Response) *APIServer {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[operation] = append(s.routes[operation], responses...)
	return s
}

// Replace sets an operation's responses, discarding anything already queued for it. Use it to
// override a default a helper registered, where appending would make the default answer first.
func (s *APIServer) Replace(operation string, responses ...Response) *APIServer {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routes[operation] = append([]Response(nil), responses...)
	return s
}

// Requests returns the calls received so far, in order.
func (s *APIServer) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// RequestsFor returns the calls received for one operation, in order.
func (s *APIServer) RequestsFor(operation string) []Request {
	var matching []Request
	for _, request := range s.Requests() {
		if s.key(request.Path) == operation {
			matching = append(matching, request)
		}
	}
	return matching
}

// LastRequest returns the most recent call, failing the test if there was none.
func (s *APIServer) LastRequest() Request {
	s.t.Helper()
	requests := s.Requests()
	if len(requests) == 0 {
		s.t.Fatal("no request was received")
	}
	return requests[len(requests)-1]
}

// CallCount returns how many calls an operation received.
func (s *APIServer) CallCount(operation string) int { return len(s.RequestsFor(operation)) }

func (s *APIServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))

	s.mu.Lock()
	s.requests = append(s.requests, Request{
		Method:        r.Method,
		Path:          r.URL.Path,
		RawPath:       r.URL.EscapedPath(),
		Query:         r.URL.RawQuery,
		Authorization: r.Header.Get("Authorization"),
		UserAgent:     r.Header.Get("User-Agent"),
		ContentType:   r.Header.Get("Content-Type"),
		Body:          string(body),
	})

	key := s.key(r.URL.Path)
	queued, ok := s.routes[key]
	var response Response
	switch {
	case !ok && s.NotFoundResponse != nil:
		response = *s.NotFoundResponse
	case !ok:
		response = Response{
			Status: http.StatusNotFound,
			Body:   `{"code":404,"message":"the test server has no route for this operation"}`,
		}
	case len(queued) == 1:
		// The last queued response repeats, which is what makes polling tests concise.
		response = queued[0]
	default:
		response = queued[0]
		s.routes[key] = queued[1:]
	}
	s.mu.Unlock()

	for name, value := range response.Header {
		w.Header().Set(name, value)
	}
	if response.Body != "" && w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "application/json")
	}
	status := response.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if response.Body != "" {
		_, _ = io.WriteString(w, response.Body)
	}
}

func (s *APIServer) key(path string) string {
	if s.UseFullPath {
		return path
	}
	trimmed := strings.TrimSuffix(path, "/")
	if index := strings.LastIndex(trimmed, "/"); index >= 0 {
		return trimmed[index+1:]
	}
	return trimmed
}
