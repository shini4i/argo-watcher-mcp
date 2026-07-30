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
//
// The field set mirrors what Argo Watcher actually serves for a task. Notably it
// carries neither `validated` nor `timeout`, for two different reasons:
// `validated` is tagged `json:"-"` upstream and so is never serialised at all,
// while `timeout` is a normal field that the Postgres state layer's
// ConvertToExternalTask does not map, leaving it absent in practice. Either way
// this server cannot observe them, and a field that always reads as its zero
// value is worse than no field — a model treats it as fact.
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
	// IsRollback reports whether this deployment returned an application to an
	// earlier image set rather than moving it forward.
	IsRollback bool `json:"is_rollback"`
	// RollbackTargetID names the earlier task whose image set a rollback
	// returned to. Empty when IsRollback is false.
	RollbackTargetID string `json:"rollback_target_id,omitempty"`
}

// DeploymentFilter captures the filters accepted by the Argo Watcher task list
// endpoint. The zero value of App and Status means "no filter".
type DeploymentFilter struct {
	App           *string
	Status        *string
	FromTimestamp int64
	ToTimestamp   int64
	// Limit bounds how many deployments are returned. Argo Watcher caps it at
	// 1000 and treats any non-positive value as that cap.
	Limit int
	// Offset skips this many matching deployments, newest first.
	Offset int
}

// DeploymentPage is one page of deployment history plus the counters needed to
// tell whether history remains beyond it.
//
// Total comes from Argo Watcher and counts every deployment matching the filter,
// ignoring Limit and Offset. Truncated reports whether deployments remain *after
// this page* — not merely whether Total exceeds the page length, which would
// also be true for an Offset past the end. It is stated explicitly rather than
// left to be derived, because a silently short page reads as a complete answer.
type DeploymentPage struct {
	Deployments []Deployment `json:"deployments"`
	Total       int64        `json:"total"`
	Limit       int          `json:"limit"`
	Offset      int          `json:"offset"`
	Truncated   bool         `json:"truncated"`
}

// DeployLockState reports whether Argo Watcher is currently refusing
// deployments, whether from a manual lock or an active scheduled lockdown.
type DeployLockState struct {
	Locked bool `json:"locked"`
}

// Reachability reports whether Argo Watcher can currently reach its own
// dependencies. Reason names the unreachable subsystem and is empty when
// Available is true.
type Reachability struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// ServerInfo describes the upstream Argo Watcher deployment: its version and
// its non-sensitive configuration.
//
// Config carries only the fields the argowatcher package allowlists, with the
// auth and notification integrations reduced to their `enabled` flag. Fields
// Argo Watcher adds later are dropped until they are deliberately allowlisted,
// so a new upstream field cannot reach an MCP client without review. Do not
// widen this to a wholesale passthrough: upstream excludes every secret today,
// but that is a property of the current release, not a guarantee about the next.
type ServerInfo struct {
	Version string         `json:"version"`
	Config  map[string]any `json:"config,omitempty"`
}

// ArgoWatcher is the read-only surface of Argo Watcher that the MCP tools
// expose. Write operations (creating tasks, setting the deploy lock) are
// deliberately absent.
type ArgoWatcher interface {
	ListDeployments(ctx context.Context, filter DeploymentFilter) (DeploymentPage, error)
	GetDeployLock(ctx context.Context) (DeployLockState, error)
	GetReachability(ctx context.Context) (Reachability, error)
	GetServerInfo(ctx context.Context) (ServerInfo, error)
}

// HealthChecker reports readiness of downstream dependencies.
type HealthChecker interface {
	Check(ctx context.Context) error
}
