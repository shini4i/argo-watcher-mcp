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
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/shini4i/argo-watcher-mcp/internal/domain"
	"github.com/shini4i/argo-watcher-mcp/internal/telemetry"
)

const tracerName = "github.com/shini4i/argo-watcher-mcp/argowatcher"

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
//
// Note this probes only Argo Watcher's own state backend; it says nothing about
// whether Argo Watcher can reach ArgoCD. GetReachability answers that.
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
	defer func() { _ = resp.Body.Close() }()

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

// getJSON issues a GET against endpoint and decodes the JSON body into out.
//
// It keeps the reachability gauge in sync with the outcome and records failures
// on whatever span is already on ctx, so callers stay responsible only for
// starting their own span and describing their own request. The label names the
// operation in error messages (for example "tasks", so failures read as "fetch
// tasks: ...").
func (c *Client) getJSON(ctx context.Context, label, endpoint string, out any) error {
	span := trace.SpanFromContext(ctx)
	fail := func(err error) error {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		c.metrics.ReportUnreachable()
		return fail(fmt.Errorf("build %s request: %w", label, err))
	}

	resp, err := c.client.Do(req)
	if err != nil {
		c.metrics.ReportUnreachable()
		return fail(fmt.Errorf("fetch %s: %w", label, err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		bodyStr := strings.TrimSpace(string(body))
		c.logger.Warn("downstream API error",
			slog.Int("status", resp.StatusCode),
			slog.String("body", bodyStr),
			slog.String("url", endpoint),
		)
		c.metrics.ReportUnreachable()
		span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))
		return fail(fmt.Errorf("fetch %s: status %d body %q", label, resp.StatusCode, bodyStr))
	}

	// The server answered, so it is reachable regardless of whether the body
	// turns out to be decodable.
	c.metrics.ReportReachable()
	span.SetAttributes(attribute.Int("http.status_code", resp.StatusCode))

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		c.logger.Error("decode response", slog.String("operation", label), slog.String("url", endpoint), slog.Any("error", err))
		return fail(fmt.Errorf("decode %s response: %w", label, err))
	}

	return nil
}

// ListDeployments fetches one page of deployment tasks matching filter, emits a
// tracing span, and maps the payload to domain objects.
func (c *Client) ListDeployments(ctx context.Context, filter domain.DeploymentFilter) (domain.DeploymentPage, error) {
	ctx, span := otel.Tracer(tracerName).Start(
		ctx,
		"client.list_deployments",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer span.End()
	span.SetAttributes(
		attribute.String("argo_watcher.base_url", c.baseURL),
		attribute.Int64("argo_watcher.from_timestamp", filter.FromTimestamp),
		attribute.Int64("argo_watcher.to_timestamp", filter.ToTimestamp),
	)
	if filter.App != nil {
		span.SetAttributes(attribute.String("argo_watcher.app", *filter.App))
	}
	if filter.Status != nil {
		span.SetAttributes(attribute.String("argo_watcher.status", *filter.Status))
	}

	endpoint, err := url.Parse(c.baseURL + "/api/v1/tasks")
	if err != nil {
		c.metrics.ReportUnreachable()
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return domain.DeploymentPage{}, fmt.Errorf("parse tasks endpoint: %w", err)
	}

	query := endpoint.Query()
	query.Set("from_timestamp", fmt.Sprintf("%d", filter.FromTimestamp))
	if filter.ToTimestamp != 0 {
		query.Set("to_timestamp", fmt.Sprintf("%d", filter.ToTimestamp))
	}
	if filter.App != nil && *filter.App != "" {
		query.Set("app", *filter.App)
	}
	if filter.Status != nil && *filter.Status != "" {
		query.Set("status", *filter.Status)
	}
	if filter.Limit > 0 {
		query.Set("limit", fmt.Sprintf("%d", filter.Limit))
	}
	if filter.Offset > 0 {
		query.Set("offset", fmt.Sprintf("%d", filter.Offset))
	}
	endpoint.RawQuery = query.Encode()
	if span.IsRecording() {
		span.SetAttributes(attribute.String("argo_watcher.request_url", endpoint.String()))
	}

	var payload struct {
		Tasks []taskPayload `json:"tasks"`
		Total int64         `json:"total"`
	}

	if err := c.getJSON(ctx, "tasks", endpoint.String(), &payload); err != nil {
		return domain.DeploymentPage{}, err
	}

	deployments := make([]domain.Deployment, 0, len(payload.Tasks))
	for _, task := range payload.Tasks {
		deployment, err := task.toDomain()
		if err != nil {
			err = fmt.Errorf("convert task %q: %w", task.ID, err)
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return domain.DeploymentPage{}, err
		}
		deployments = append(deployments, deployment)
	}

	// Argo Watcher counts every match before applying limit/offset, so `total`
	// is authoritative and is taken at face value. It is tagged omitempty
	// upstream, so "no matches" arrives as an absent field and decodes to 0 —
	// which is the correct total in that case. Argo Watcher has reported `total`
	// since v0.10.0; against anything older it would be absent alongside real
	// rows and undercount, hence the documented version floor in the README.
	covered := int64(filter.Offset) + int64(len(deployments))

	page := domain.DeploymentPage{
		Deployments: deployments,
		Total:       payload.Total,
		Limit:       filter.Limit,
		Offset:      filter.Offset,
		Truncated:   covered < payload.Total,
	}

	span.SetAttributes(
		attribute.Int("argo_watcher.result_count", len(deployments)),
		attribute.Int64("argo_watcher.total", page.Total),
		attribute.Bool("argo_watcher.truncated", page.Truncated),
	)
	span.SetStatus(codes.Ok, "completed")

	return page, nil
}

// GetDeployLock reports whether Argo Watcher is currently refusing deployments.
func (c *Client) GetDeployLock(ctx context.Context) (domain.DeployLockState, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "client.get_deploy_lock", trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()

	// The endpoint returns a bare JSON boolean, not an object.
	var locked bool
	if err := c.getJSON(ctx, "deploy lock", c.baseURL+"/api/v1/deploy-lock", &locked); err != nil {
		return domain.DeployLockState{}, err
	}

	span.SetAttributes(attribute.Bool("argo_watcher.deploy_locked", locked))
	span.SetStatus(codes.Ok, "completed")

	return domain.DeployLockState{Locked: locked}, nil
}

// GetReachability reports whether Argo Watcher can currently reach ArgoCD and
// its state backend.
func (c *Client) GetReachability(ctx context.Context) (domain.Reachability, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "client.get_reachability", trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()

	var reachability domain.Reachability
	if err := c.getJSON(ctx, "reachability", c.baseURL+"/api/v1/reachability", &reachability); err != nil {
		return domain.Reachability{}, err
	}

	span.SetAttributes(attribute.Bool("argo_watcher.available", reachability.Available))
	if reachability.Reason != "" {
		span.SetAttributes(attribute.String("argo_watcher.unavailable_reason", reachability.Reason))
	}
	span.SetStatus(codes.Ok, "completed")

	return reachability, nil
}

// GetServerInfo fetches the upstream version and non-sensitive configuration.
//
// Both calls must succeed: a partial answer would leave the caller unable to
// tell a missing field from an unset one.
func (c *Client) GetServerInfo(ctx context.Context) (domain.ServerInfo, error) {
	ctx, span := otel.Tracer(tracerName).Start(ctx, "client.get_server_info", trace.WithSpanKind(trace.SpanKindInternal))
	defer span.End()

	// The version endpoint returns a bare JSON string.
	var version string
	if err := c.getJSON(ctx, "version", c.baseURL+"/api/v1/version", &version); err != nil {
		return domain.ServerInfo{}, err
	}

	var cfg map[string]any
	if err := c.getJSON(ctx, "config", c.baseURL+"/api/v1/config", &cfg); err != nil {
		return domain.ServerInfo{}, err
	}

	span.SetAttributes(attribute.String("argo_watcher.version", version))
	span.SetStatus(codes.Ok, "completed")

	return domain.ServerInfo{Version: version, Config: c.projectConfig(cfg)}, nil
}

// exposedConfigKeys lists the /api/v1/config fields forwarded to MCP clients.
//
// Argo Watcher tags every field it considers a credential `json:"-"`, so no
// declared secret reaches this client. The allowlist guards the next release
// rather than this one: forwarding the payload wholesale would mean any field
// upstream adds later — a webhook URL, a state-backend DSN, a token — lands in
// an LLM's context and in client transcripts with no code change and no review.
// An allowlist turns that into a deliberate edit.
//
// Being allowlisted is not the same as being safe to forward verbatim: see
// urlValuedConfigKeys for fields whose value can itself embed a credential.
var exposedConfigKeys = map[string]struct{}{
	"argo_cd_url":          {},
	"argo_cd_url_alias":    {},
	"argo_api_timeout":     {},
	"argo_api_retries":     {},
	"argo_refresh_app":     {},
	"accept_suspended_app": {},
	"deployment_timeout":   {},
	"registry_proxy_url":   {},
	"state_type":           {},
	"skip_tls_verify":      {},
	"log_level":            {},
	"lockdown_schedule":    {},
}

// integrationKeys names nested config objects reduced to their `enabled` flag.
// Their remaining fields describe how to reach a third-party service and are the
// likeliest future home of a credential, so only the flag is forwarded.
var integrationKeys = []string{"oidc", "webhook", "mattermost"}

// urlValuedConfigKeys are allowlisted fields whose value is a URL string Argo
// Watcher takes verbatim from the environment (ARGO_URL_ALIAS and
// DOCKER_IMAGES_PROXY). Neither is redacted upstream, and a registry proxy or an
// externally-published ArgoCD URL can legitimately be written with basic-auth
// userinfo — `https://user:password@host`. Forwarding that would hand the
// credential to an LLM, so the userinfo is stripped before the value leaves here.
//
// argo_cd_url needs no entry: it arrives as a decoded net/url.URL object and
// flattenURL rebuilds it without the User field.
var urlValuedConfigKeys = []string{"argo_cd_url_alias", "registry_proxy_url"}

// projectConfig reduces the upstream config payload to the allowlisted fields.
//
// Dropped keys are logged at debug level. The allowlist is matched against
// Argo Watcher's JSON tags by hand, so a rename upstream would otherwise empty
// the config with no signal at all — this turns that into one grep.
func (c *Client) projectConfig(cfg map[string]any) map[string]any {
	if cfg == nil {
		return nil
	}

	projected := make(map[string]any, len(exposedConfigKeys)+len(integrationKeys))
	for key, value := range cfg {
		if _, ok := exposedConfigKeys[key]; ok {
			projected[key] = value
		}
	}

	// Argo Watcher declares argo_cd_url as a net/url.URL, which implements no
	// MarshalJSON or MarshalText, so encoding/json emits it field by field as
	// {"Scheme":...,"Host":...,"Path":...}. Flatten it back to a URL string: the
	// point of exposing it is so callers can build links to ArgoCD, and a struct
	// of Go field names makes that a reassembly puzzle.
	if raw, ok := projected[argoURLKey]; ok {
		if flattened, ok := flattenURL(raw); ok {
			projected[argoURLKey] = flattened
		}
	}

	for _, key := range urlValuedConfigKeys {
		raw, present := projected[key]
		if !present {
			continue
		}
		// Anything but a string means upstream changed the field's shape. Drop it
		// rather than forward a value this code cannot reason about.
		asString, ok := raw.(string)
		if !ok {
			delete(projected, key)
			continue
		}
		redacted, ok := redactURLUserinfo(asString)
		if !ok {
			delete(projected, key)
			continue
		}
		projected[key] = redacted
	}

	for _, key := range integrationKeys {
		nested, ok := cfg[key].(map[string]any)
		if !ok {
			continue
		}
		if enabled, ok := nested["enabled"]; ok {
			projected[key] = map[string]any{"enabled": enabled}
		}
	}

	if dropped := droppedKeys(cfg, projected); len(dropped) > 0 {
		c.logger.Debug("config fields not allowlisted", slog.Any("keys", dropped))
	}

	return projected
}

// argoURLKey is the config field carrying the ArgoCD base URL.
const argoURLKey = "argo_cd_url"

// redactURLUserinfo strips any `user:password@` component from a URL string,
// leaving the rest intact so the value stays useful for building links.
//
// It reports false for a value that does not parse as a URL: an unparseable
// string cannot be sanitised with any confidence, and dropping it is preferable
// to forwarding something that might carry a credential in a shape this code
// does not recognise.
func redactURLUserinfo(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if parsed.User == nil {
		return raw, true
	}

	parsed.User = nil
	return parsed.String(), true
}

// flattenURL renders a decoded net/url.URL object back into a URL string. It
// reports false when the value is not that shape — including when it is already
// a plain string — so the caller leaves it untouched.
func flattenURL(value any) (string, bool) {
	fields, ok := value.(map[string]any)
	if !ok {
		return "", false
	}

	str := func(key string) string {
		s, _ := fields[key].(string)
		return s
	}

	parsed := url.URL{
		Scheme:   str("Scheme"),
		Opaque:   str("Opaque"),
		Host:     str("Host"),
		Path:     str("Path"),
		RawQuery: str("RawQuery"),
		Fragment: str("Fragment"),
	}
	if parsed.Scheme == "" && parsed.Host == "" && parsed.Path == "" {
		return "", false
	}

	return parsed.String(), true
}

// droppedKeys lists the config keys the projection did not forward, sorted so
// the log line is stable across requests.
func droppedKeys(cfg, projected map[string]any) []string {
	dropped := make([]string, 0, len(cfg))
	for key := range cfg {
		if _, kept := projected[key]; !kept {
			dropped = append(dropped, key)
		}
	}
	sort.Strings(dropped)
	return dropped
}

type taskPayload struct {
	ID               string         `json:"id"`
	App              string         `json:"app"`
	Author           string         `json:"author"`
	Project          string         `json:"project"`
	Images           []imagePayload `json:"images"`
	Status           string         `json:"status"`
	Created          jsonTimestamp  `json:"created"`
	Updated          jsonTimestamp  `json:"updated"`
	StatusReason     *string        `json:"status_reason"`
	IsRollback       bool           `json:"is_rollback"`
	RollbackTargetID string         `json:"rollback_target_id"`
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
		ID:               p.ID,
		App:              p.App,
		Author:           p.Author,
		Project:          p.Project,
		Images:           images,
		Status:           p.Status,
		Created:          p.Created.Time,
		Updated:          p.Updated.Time,
		StatusReason:     p.StatusReason,
		IsRollback:       p.IsRollback,
		RollbackTargetID: p.RollbackTargetID,
	}, nil
}

// jsonTimestamp decodes the timestamp shapes Argo Watcher's task endpoint
// actually produces: Unix seconds (models.Task declares Created/Updated as
// float64, and both state backends store seconds — the Postgres layer via
// ConvertToExternalTask's .Unix(), the in-memory one via time.Now().Unix()), plus
// RFC3339 strings for tolerance.
//
// It deliberately does not try to guess millisecond, microsecond or nanosecond
// epochs by magnitude. Nothing upstream emits them on a read path, and a
// magnitude heuristic silently misdates values it guesses wrong about.
type jsonTimestamp struct {
	time.Time
}

// UnmarshalJSON accepts an RFC3339 string or a Unix-seconds number, whole or
// fractional. A null or empty value decodes to the zero time, which callers
// treat as "missing".
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

	t.Time = time.Unix(value, 0).UTC()
	return nil
}
