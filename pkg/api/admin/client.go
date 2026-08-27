package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/admin/internal/generated"
)

// Config describes a connection to one environment's Admin API.
type Config struct {
	// BaseURL is the complete Admin API base URL. It has no default: it is not derived from an
	// environment ID or a region, because that would bake today's
	// "<env>.env.<region>.restate.cloud:9070" hostname shape into the provider.
	BaseURL string
	// APIKey is an environment-scoped API key. It is sent to BaseURL and to nothing else. It must
	// never be an account-scoped management key.
	APIKey string
	// UserAgent identifies the provider and Terraform versions.
	UserAgent string
	// Timeout bounds one request including its retries. Deployment registration performs service
	// discovery against the user's own endpoint, so callers give it a generous value.
	Timeout time.Duration
	// Retry bounds retrying.
	Retry api.RetryPolicy
	// AllowInsecureHTTP permits a plaintext base URL for a non-loopback host, for a self-hosted or
	// local Restate.
	AllowInsecureHTTP bool

	// HTTPTransport is the underlying round tripper. Tests substitute one.
	HTTPTransport http.RoundTripper
}

// Client is a concurrency-safe, immutable client for one environment's Admin API.
type Client struct {
	baseURL   string
	generated *generated.Client
	executor  *api.Executor
}

// New validates cfg and returns a client. It performs no network I/O; version negotiation is a
// separate, explicit step.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("an Admin API base URL is required")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("an environment API key is required")
	}

	normalized, err := api.NormalizeBaseURL(cfg.BaseURL)
	if err != nil {
		return nil, err
	}

	httpClient, err := api.NewTransport(api.TransportConfig{
		BaseURL:           normalized,
		BearerToken:       cfg.APIKey,
		UserAgent:         cfg.UserAgent,
		Timeout:           cfg.Timeout,
		Retry:             cfg.Retry,
		AllowInsecureHTTP: cfg.AllowInsecureHTTP,
		Base:              cfg.HTTPTransport,
	})
	if err != nil {
		return nil, err
	}

	client, err := generated.NewClient(normalized, generated.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("build Admin API client: %w", err)
	}

	return &Client{
		baseURL:   normalized,
		generated: client,
		executor: &api.Executor{
			Service:     api.ServiceAdmin,
			DecodeError: decodeError,
		},
	}, nil
}

// BaseURL is the normalized endpoint this client talks to.
func (c *Client) BaseURL() string { return c.baseURL }

// decodeError reads the Admin API's error body, which differs from the Cloud API's:
// `{"message": <string>, "restate_code": <string>}`.
//
// The /query endpoint uses a body with only a message, which this also handles, and a gateway in
// front of the environment can substitute a non-JSON error page, which must not become a decode
// failure that hides the status.
func decodeError(_ int, body []byte) (code, message string) {
	var decoded struct {
		Message     string `json:"message"`
		RestateCode string `json:"restate_code"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", ""
	}
	return decoded.RestateCode, api.RedactString(decoded.Message)
}

// stringPointer returns a pointer to s, or nil when s is empty, which is how an unset optional
// string is omitted from a request body rather than sent as "".
func stringPointer(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// truePointer returns a pointer to true when set, and nil otherwise, so a false flag is omitted
// rather than sent explicitly. The contract defaults several booleans to values that are not
// Go's zero value, so sending an explicit false would change behaviour.
func truePointer(set bool) *bool {
	if !set {
		return nil
	}
	value := true
	return &value
}

// mapPointer returns a pointer to a copy of m, or nil when m is nil.
//
// The nil/empty distinction is deliberate and load-bearing: nil means "do not send this field",
// which leaves the server's value alone, while an empty map means "set this to no entries". Copying
// prevents a caller's later mutation from reaching an in-flight request.
func mapPointer(m map[string]string) *map[string]string {
	if m == nil {
		return nil
	}
	copied := make(map[string]string, len(m))
	for key, value := range m {
		copied[key] = value
	}
	return &copied
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func derefMap(m *map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	copied := make(map[string]string, len(*m))
	for key, value := range *m {
		copied[key] = value
	}
	return copied
}

func noticesFrom(info *[]generated.SchemaInfo) []Notice {
	if info == nil || len(*info) == 0 {
		return nil
	}
	notices := make([]Notice, 0, len(*info))
	for _, entry := range *info {
		notices = append(notices, Notice{Code: derefString(entry.Code), Message: entry.Message})
	}
	return notices
}
