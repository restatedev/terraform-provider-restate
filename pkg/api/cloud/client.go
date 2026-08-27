package cloud

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/restatedev/terraform-provider-restate/pkg/api"
	"github.com/restatedev/terraform-provider-restate/pkg/api/cloud/internal/generated"
)

// DefaultBaseURL is the Cloud Management API's canonical URL. The "/cloud" suffix is part of the
// endpoint, not an artefact: operation paths are appended to it.
//
// It is not inferred from an environment's region. An environment lives in a region; the management
// API that provisions it does not.
const DefaultBaseURL = "https://api.us.restate.cloud/cloud"

// Config describes a connection to the Cloud Management API for one account.
type Config struct {
	// BaseURL defaults to DefaultBaseURL when empty.
	BaseURL string
	// AccountID is the account every operation is scoped to. Required.
	AccountID string
	// ManagementAPIKey is an account-scoped key with role rst:role::ManagementApiAccess. Required, and
	// sent to BaseURL and nowhere else.
	ManagementAPIKey string
	// UserAgent identifies the provider and Terraform versions.
	UserAgent string
	// Timeout is the default deadline for one request including its retries. It applies only when the
	// caller's context has no deadline of its own, so a resource-level timeout always wins.
	Timeout time.Duration
	// Retry bounds retrying. The zero value disables it.
	Retry api.RetryPolicy
	// Poll bounds the asynchronous environment lifecycle loops.
	Poll api.PollConfig
	// AllowInsecureHTTP permits a plaintext base URL for a non-loopback host. For testing only.
	AllowInsecureHTTP bool

	// HTTPTransport is the underlying round tripper. Tests substitute one.
	HTTPTransport http.RoundTripper
}

// Client is a concurrency-safe, immutable client for one account. Every operation is account-scoped,
// so an account ID cannot be passed to the wrong call.
type Client struct {
	accountID string
	baseURL   string
	generated *generated.Client
	executor  *api.Executor
	poll      api.PollConfig
}

// New validates cfg and returns a client. It performs no network I/O; credential validation is a
// separate, explicit step.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.AccountID) == "" {
		return nil, errors.New("an account ID is required")
	}
	if strings.TrimSpace(cfg.ManagementAPIKey) == "" {
		return nil, errors.New("a management API key is required")
	}

	baseURL := cfg.BaseURL
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultBaseURL
	}
	normalized, err := api.NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, err
	}

	httpClient, err := api.NewTransport(api.TransportConfig{
		BaseURL:           normalized,
		BearerToken:       cfg.ManagementAPIKey,
		UserAgent:         cfg.UserAgent,
		Retry:             cfg.Retry,
		AllowInsecureHTTP: cfg.AllowInsecureHTTP,
		Base:              cfg.HTTPTransport,
	})
	if err != nil {
		return nil, err
	}

	client, err := generated.NewClient(normalized, generated.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("build Cloud Management API client: %w", err)
	}

	poll := cfg.Poll
	if poll.Interval <= 0 {
		poll.Interval = api.DefaultPollConfig().Interval
	}

	return &Client{
		accountID: strings.TrimSpace(cfg.AccountID),
		baseURL:   normalized,
		generated: client,
		executor: &api.Executor{
			Service:        api.ServiceCloud,
			DecodeError:    decodeError,
			RequestTimeout: cfg.Timeout,
		},
		poll: poll,
	}, nil
}

// AccountID is the account this client is scoped to.
func (c *Client) AccountID() string { return c.accountID }

// BaseURL is the normalized endpoint this client talks to.
func (c *Client) BaseURL() string { return c.baseURL }

// decodeError reads the Cloud API's error body, which every documented error response shares:
// `{"code": <number>, "message": <string>}`. A gateway in front of the API can substitute an HTML
// error page, so a non-JSON body must not become a decode failure that hides the status.
func decodeError(status int, body []byte) (code, message string) {
	var decoded struct {
		Code    json.Number `json:"code"`
		Message string      `json:"message"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", ""
	}

	code = decoded.Code.String()
	// Every documented value repeats the HTTP status, so echoing it alongside "HTTP 403" is noise.
	if code == strconv.Itoa(status) {
		code = ""
	}
	return code, api.RedactString(decoded.Message)
}
