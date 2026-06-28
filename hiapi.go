package hiapi

import (
	"errors"
	"net/http"
	"os"
	"time"
)

// Default client configuration.
const (
	DefaultBaseURL    = "https://api.hiapi.ai/v1"
	DefaultTimeout    = 60 * time.Second
	DefaultMaxRetries = 2
)

// Client is the entry point to the HiAPI SDK. Construct it with [New].
//
// A Client is safe for concurrent use by multiple goroutines.
type Client struct {
	apiKey  string
	baseURL string

	transport *transport

	// Tasks is the unified async task API (/v1/tasks).
	Tasks *Tasks
	// Webhooks verifies inbound callback signatures.
	Webhooks *Webhooks
}

// Option configures a [Client] in [New].
type Option func(*config)

type config struct {
	baseURL       string
	timeout       time.Duration
	maxRetries    int
	webhookSecret string
	httpClient    *http.Client
}

// WithBaseURL overrides the API base URL. A missing /v1 suffix is added.
func WithBaseURL(u string) Option { return func(c *config) { c.baseURL = u } }

// WithTimeout sets the per-request timeout. Ignored if WithHTTPClient is used.
func WithTimeout(d time.Duration) Option { return func(c *config) { c.timeout = d } }

// WithMaxRetries sets the retry budget for 429/503 and (idempotent) network
// errors. Negative values are clamped to zero.
func WithMaxRetries(n int) Option { return func(c *config) { c.maxRetries = n } }

// WithWebhookSecret sets the default signing key for Client.Webhooks.Verify.
// Falls back to the HIAPI_WEBHOOK_SECRET environment variable when unset.
func WithWebhookSecret(secret string) Option {
	return func(c *config) { c.webhookSecret = secret }
}

// WithHTTPClient supplies a custom *http.Client (for proxies, custom TLS, or
// transport-level instrumentation). Its Timeout, if any, is respected as-is and
// WithTimeout is ignored.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *config) { c.httpClient = hc }
}

// New creates a Client.
//
// apiKey is the account API key (sk-...); if empty, it falls back to the
// HIAPI_API_KEY environment variable. New returns an error when no key can be
// resolved.
func New(apiKey string, opts ...Option) (*Client, error) {
	key := apiKey
	if key == "" {
		key = os.Getenv("HIAPI_API_KEY")
	}
	if key == "" {
		return nil, errors.New(
			"hiapi: missing API key: pass it to New or set the HIAPI_API_KEY " +
				"environment variable",
		)
	}

	cfg := config{
		baseURL:    DefaultBaseURL,
		timeout:    DefaultTimeout,
		maxRetries: DefaultMaxRetries,
	}
	for _, opt := range opts {
		opt(&cfg)
	}

	httpClient := cfg.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: cfg.timeout}
	}

	maxRetries := cfg.maxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}

	base := normalizeBaseURL(cfg.baseURL)
	tr := &transport{
		apiKey:     key,
		baseURL:    base,
		maxRetries: maxRetries,
		httpClient: httpClient,
	}

	secret := cfg.webhookSecret
	if secret == "" {
		secret = os.Getenv("HIAPI_WEBHOOK_SECRET")
	}

	c := &Client{apiKey: key, baseURL: base, transport: tr}
	c.Tasks = &Tasks{transport: tr}
	c.Webhooks = &Webhooks{secret: secret}
	return c, nil
}
