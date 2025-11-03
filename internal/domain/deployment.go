package domain

import (
	"context"
	"time"
)

// Image represents a container image used in a deployment task.
type Image struct {
	Image string `json:"image"`
	Tag   string `json:"tag"`
}

// Deployment models a task entry returned by the Argo Watcher API.
type Deployment struct {
	ID           string    `json:"id"`
	App          string    `json:"app"`
	Author       string    `json:"author"`
	Project      string    `json:"project"`
	Images       []Image   `json:"images"`
	Status       string    `json:"status"`
	Created      time.Time `json:"created"`
	Updated      time.Time `json:"updated"`
	StatusReason *string   `json:"status_reason,omitempty"`
	Validated    bool      `json:"validated"`
	Timeout      *int      `json:"timeout,omitempty"`
}

// DeploymentFilter captures optional filters when requesting deployments.
type DeploymentFilter struct {
	App           *string
	FromTimestamp int64
	ToTimestamp   int64
}

// DeploymentService fetches deployments according to the provided filter.
type DeploymentService interface {
	ListDeployments(ctx context.Context, filter DeploymentFilter) ([]Deployment, error)
}

// HealthChecker reports readiness of downstream dependencies.
type HealthChecker interface {
	Check(ctx context.Context) error
}
