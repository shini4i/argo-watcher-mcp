package argowatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shini4i/argo-watcher-mcp/internal/domain"
	"github.com/shini4i/argo-watcher-mcp/internal/telemetry"
)

// HTTPClient models the subset of http.Client used by the Argo Watcher client.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client interacts with the upstream Argo Watcher service.
type Client struct {
	baseURL string
	client  HTTPClient
	logger  *slog.Logger
	metrics telemetry.ArgoWatcherReachability
}

// Option customizes the behaviour of the Argo Watcher client.
type Option func(*Client)

// WithReachabilityMetrics wires reachability instrumentation into the client.
func WithReachabilityMetrics(metrics telemetry.ArgoWatcherReachability) Option {
	return func(c *Client) {
		if metrics != nil {
			c.metrics = metrics
		}
	}
}

// New creates a Client.
func New(baseURL string, client HTTPClient, logger *slog.Logger, options ...Option) *Client {
	if logger == nil {
		logger = slog.Default()
	}
	c := &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  client,
		logger:  logger,
		metrics: telemetry.NoopArgoWatcherReachability(),
	}

	for _, apply := range options {
		if apply != nil {
			apply(c)
		}
	}

	return c
}

// Check verifies the downstream service is responding on /healthz.
func (c *Client) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/healthz", nil)
	if err != nil {
		c.metrics.ReportUnreachable()
		return fmt.Errorf("build health request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		c.metrics.ReportUnreachable()
		return fmt.Errorf("health request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		c.metrics.ReportUnreachable()
		return fmt.Errorf("health request: unexpected status %d", resp.StatusCode)
	}

	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		c.metrics.ReportUnreachable()
		return fmt.Errorf("drain response body: %w", err)
	}
	c.metrics.ReportReachable()
	return nil
}

// ListDeployments fetches deployment tasks from the API and maps them to domain objects.
func (c *Client) ListDeployments(ctx context.Context, filter domain.DeploymentFilter) ([]domain.Deployment, error) {
	endpoint, err := url.Parse(c.baseURL + "/api/v1/tasks")
	if err != nil {
		c.metrics.ReportUnreachable()
		return nil, fmt.Errorf("parse tasks endpoint: %w", err)
	}

	query := endpoint.Query()
	query.Set("from_timestamp", fmt.Sprintf("%d", filter.FromTimestamp))
	if filter.ToTimestamp != 0 {
		query.Set("to_timestamp", fmt.Sprintf("%d", filter.ToTimestamp))
	}
	if filter.App != nil && *filter.App != "" {
		query.Set("app", *filter.App)
	}
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		c.metrics.ReportUnreachable()
		return nil, fmt.Errorf("build tasks request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		c.metrics.ReportUnreachable()
		return nil, fmt.Errorf("fetch tasks: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		bodyStr := strings.TrimSpace(string(body))
		c.logger.Warn("downstream API error",
			slog.Int("status", resp.StatusCode),
			slog.String("body", bodyStr),
			slog.String("url", endpoint.String()),
		)
		c.metrics.ReportUnreachable()
		return nil, fmt.Errorf("fetch tasks: status %d body %q", resp.StatusCode, bodyStr)
	}
	c.metrics.ReportReachable()

	var payload struct {
		Tasks []taskPayload `json:"tasks"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		c.logger.Error("decode tasks response", slog.Any("error", err))
		return nil, fmt.Errorf("decode tasks response: %w", err)
	}

	deployments := make([]domain.Deployment, 0, len(payload.Tasks))
	for _, task := range payload.Tasks {
		deployment, err := task.toDomain()
		if err != nil {
			return nil, fmt.Errorf("convert task %q: %w", task.ID, err)
		}
		deployments = append(deployments, deployment)
	}

	return deployments, nil
}

type taskPayload struct {
	ID           string          `json:"id"`
	App          string          `json:"app"`
	Author       string          `json:"author"`
	Project      string          `json:"project"`
	Images       []imagePayload  `json:"images"`
	Status       string          `json:"status"`
	Created      jsonTimestamp   `json:"created"`
	Updated      jsonTimestamp   `json:"updated"`
	StatusReason *string         `json:"status_reason"`
	Validated    bool            `json:"validated"`
	Timeout      *int            `json:"timeout"`
	Raw          json.RawMessage `json:"-"`
}

type imagePayload struct {
	Image string `json:"image"`
	Tag   string `json:"tag"`
}

func (p taskPayload) toDomain() (domain.Deployment, error) {
	images := make([]domain.Image, 0, len(p.Images))
	for _, img := range p.Images {
		if img.Image == "" && img.Tag == "" {
			continue
		}
		images = append(images, domain.Image{
			Image: img.Image,
			Tag:   img.Tag,
		})
	}

	if p.Created.IsZero() {
		return domain.Deployment{}, fmt.Errorf("missing created timestamp")
	}
	if p.Updated.IsZero() {
		return domain.Deployment{}, fmt.Errorf("missing updated timestamp")
	}

	return domain.Deployment{
		ID:           p.ID,
		App:          p.App,
		Author:       p.Author,
		Project:      p.Project,
		Images:       images,
		Status:       p.Status,
		Created:      p.Created.Time,
		Updated:      p.Updated.Time,
		StatusReason: p.StatusReason,
		Validated:    p.Validated,
		Timeout:      p.Timeout,
	}, nil
}

// jsonTimestamp decodes timestamp fields that can be represented as RFC3339 strings or Unix epoch numbers.
type jsonTimestamp struct {
	time.Time
}

// UnmarshalJSON accepts RFC3339 strings, Unix seconds, or Unix millisecond/microsecond/nanosecond integers.
func (t *jsonTimestamp) UnmarshalJSON(data []byte) error {
	token := strings.TrimSpace(string(data))
	if token == "" || token == "null" {
		t.Time = time.Time{}
		return nil
	}

	if strings.HasPrefix(token, "\"") {
		var ts string
		if err := json.Unmarshal(data, &ts); err != nil {
			return fmt.Errorf("unmarshal timestamp string: %w", err)
		}
		if ts == "" {
			t.Time = time.Time{}
			return nil
		}
		parsed, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			return fmt.Errorf("parse RFC3339 timestamp %q: %w", ts, err)
		}
		t.Time = parsed
		return nil
	}

	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return fmt.Errorf("unmarshal numeric timestamp: %w", err)
	}

	// Prefer preserving fractional seconds when present.
	if strings.Contains(number.String(), ".") {
		value, err := number.Float64()
		if err != nil {
			return fmt.Errorf("convert fractional timestamp %q: %w", number.String(), err)
		}
		secs, frac := math.Modf(value)
		t.Time = time.Unix(int64(secs), int64(frac*float64(time.Second))).UTC()
		return nil
	}

	value, err := number.Int64()
	if err != nil {
		return fmt.Errorf("convert integral timestamp %q: %w", number.String(), err)
	}

	switch {
	case value > 1_000_000_000_000_000_000: // nanoseconds
		t.Time = time.Unix(0, value).UTC()
	case value > 1_000_000_000_000_000: // microseconds
		t.Time = time.Unix(0, value*int64(time.Microsecond)).UTC()
	case value > 1_000_000_000_000: // milliseconds
		t.Time = time.Unix(0, value*int64(time.Millisecond)).UTC()
	default: // seconds
		t.Time = time.Unix(value, 0).UTC()
	}
	return nil
}
