package argowatcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/shini4i/argo-watcher-mcp/internal/domain"
)

// HTTPClient models the subset of http.Client used by the Argo Watcher client.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client interacts with the upstream Argo Watcher service.
type Client struct {
	baseURL string
	client  HTTPClient
}

// New creates a Client.
func New(baseURL string, client HTTPClient) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  client,
	}
}

// Check verifies the downstream service is responding on /healthz.
func (c *Client) Check(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/healthz", nil)
	if err != nil {
		return fmt.Errorf("build health request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("health request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("health request: unexpected status %d", resp.StatusCode)
	}

	io.Copy(io.Discard, resp.Body)
	return nil
}

// ListDeployments fetches deployment tasks from the API and maps them to domain objects.
func (c *Client) ListDeployments(ctx context.Context, filter domain.DeploymentFilter) ([]domain.Deployment, error) {
	endpoint, err := url.Parse(c.baseURL + "/api/v1/tasks")
	if err != nil {
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
		return nil, fmt.Errorf("build tasks request: %w", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch tasks: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("fetch tasks: status %d body %q", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Tasks []taskPayload `json:"tasks"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
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
	Created      time.Time       `json:"created"`
	Updated      time.Time       `json:"updated"`
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
		Created:      p.Created,
		Updated:      p.Updated,
		StatusReason: p.StatusReason,
		Validated:    p.Validated,
		Timeout:      p.Timeout,
	}, nil
}
