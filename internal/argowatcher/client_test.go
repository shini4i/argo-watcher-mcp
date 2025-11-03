package argowatcher

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/shini4i/argo-watcher-mcp/internal/domain"
)

func TestCheckSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := New(srv.URL, srv.Client(), nil)
	if err := client.Check(context.Background()); err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func TestCheckFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	client := New(srv.URL, srv.Client(), nil)
	if err := client.Check(context.Background()); err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestListDeployments(t *testing.T) {
	requested := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/tasks" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}

		values := r.URL.Query()
		if values.Get("app") != "api" {
			t.Fatalf("expected app query api, got %s", values.Get("app"))
		}
		if values.Get("from_timestamp") != "10" {
			t.Fatalf("expected from_timestamp 10, got %s", values.Get("from_timestamp"))
		}
		if values.Get("to_timestamp") != "20" {
			t.Fatalf("expected to_timestamp 20, got %s", values.Get("to_timestamp"))
		}

		response := map[string]any{
			"tasks": []any{
				map[string]any{
					"id":            "task-1",
					"app":           "api",
					"author":        "alice",
					"project":       "proj",
					"images":        []any{map[string]any{"image": "repo", "tag": "v1"}},
					"status":        "Success",
					"created":       time.Unix(10, 0).UTC(),
					"updated":       time.Unix(20, 0).UTC(),
					"status_reason": nil,
					"validated":     true,
				},
			},
		}

		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Fatalf("encode response: %v", err)
		}
		requested <- struct{}{}
	}))
	defer srv.Close()

	client := New(srv.URL, srv.Client(), nil)
	app := "api"
	deployments, err := client.ListDeployments(context.Background(), domain.DeploymentFilter{
		App:           &app,
		FromTimestamp: 10,
		ToTimestamp:   20,
	})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if len(deployments) != 1 {
		t.Fatalf("expected one deployment, got %d", len(deployments))
	}

	select {
	case <-requested:
	default:
		t.Fatalf("request was not received")
	}
}

func TestListDeploymentsHandlesNonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte("upstream failed"))
	}))
	defer srv.Close()

	client := New(srv.URL, srv.Client(), nil)
	if _, err := client.ListDeployments(context.Background(), domain.DeploymentFilter{}); err == nil {
		t.Fatal("expected error for non-success status")
	}
}

func TestListDeploymentsDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("not-json"))
	}))
	defer srv.Close()

	client := New(srv.URL, srv.Client(), nil)
	if _, err := client.ListDeployments(context.Background(), domain.DeploymentFilter{}); err == nil {
		t.Fatal("expected error decoding payload")
	}
}

func TestListDeploymentsToDomainError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"tasks": []any{
				map[string]any{
					"id":      "task-1",
					"app":     "app",
					"author":  "author",
					"project": "proj",
				},
			},
		})
	}))
	defer srv.Close()

	client := New(srv.URL, srv.Client(), nil)
	if _, err := client.ListDeployments(context.Background(), domain.DeploymentFilter{}); err == nil {
		t.Fatal("expected error when payload missing timestamps")
	}
}
