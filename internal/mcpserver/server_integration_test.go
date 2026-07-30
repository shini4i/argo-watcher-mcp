package mcpserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/shini4i/argo-watcher-mcp/internal/clock"
	"github.com/shini4i/argo-watcher-mcp/internal/domain"
)

func TestServerIntegrationCallTool(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	now := time.Date(2024, time.March, 10, 15, 4, 5, 0, time.UTC)
	deployments := []domain.Deployment{
		{
			ID:               "deployment-1",
			App:              "demo",
			Author:           "alice",
			Project:          "kube",
			Images:           []domain.Image{{Image: "registry/demo", Tag: "v1"}},
			Status:           "deployed",
			Created:          now,
			Updated:          now,
			IsRollback:       true,
			RollbackTargetID: "deployment-0",
		},
	}
	expected := domain.DeploymentPage{
		Deployments: deployments,
		Total:       12,
		Limit:       defaultDeploymentLimit,
		Offset:      0,
		Truncated:   true,
	}

	service := &stubArgoWatcher{
		result:    deployments,
		total:     12,
		truncated: true,
	}

	srv, err := New(Options{
		Name:    "integration-test",
		Version: "0.0.1",
		Service: service,
		Clock:   clock.FixedClock{At: now},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	serverSession, err := srv.MCP().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect() error = %v", err)
	}
	t.Cleanup(func() {
		// Wait for the server session to finish once the client closes.
		_ = serverSession.Wait()
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "integration-client"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = clientSession.Wait()
	})

	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
		Name:      "get_deployments",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}

	payload, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}

	var got domain.DeploymentPage
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}

	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("unexpected tool output (-want +got):\n%s", diff)
	}
}

// The read-only tools that take no arguments must be reachable over a real MCP
// session, since their empty input schema is generated rather than hand-written.
func TestServerIntegrationNoArgumentTools(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	service := &stubArgoWatcher{
		lock:         domain.DeployLockState{Locked: true},
		reachability: domain.Reachability{Available: false, Reason: "argocd"},
		info: domain.ServerInfo{
			Version: "1.2.3",
			Config:  map[string]any{"argo_cd_url": "https://argocd.example.com"},
		},
	}

	srv, err := New(Options{Name: "integration-test", Version: "0.0.1", Service: service})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := srv.MCP().Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatalf("server.Connect() error = %v", err)
	}
	t.Cleanup(func() { _ = serverSession.Wait() })

	client := mcp.NewClient(&mcp.Implementation{Name: "integration-client"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect() error = %v", err)
	}
	t.Cleanup(func() {
		_ = clientSession.Close()
		_ = clientSession.Wait()
	})

	testCases := []struct {
		tool string
		want any
	}{
		{tool: "get_deploy_lock", want: service.lock},
		{tool: "get_reachability", want: service.reachability},
		{tool: "get_server_info", want: service.info},
	}

	for _, tc := range testCases {
		t.Run(tc.tool, func(t *testing.T) {
			result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{
				Name:      tc.tool,
				Arguments: map[string]any{},
			})
			if err != nil {
				t.Fatalf("CallTool(%s) error = %v", tc.tool, err)
			}
			if result.IsError {
				t.Fatalf("CallTool(%s) returned a tool error: %+v", tc.tool, result.Content)
			}

			// Compare as decoded JSON: structured content arrives as a map, so
			// field order carries no meaning.
			want, err := roundTripJSON(tc.want)
			if err != nil {
				t.Fatalf("round-trip expectation: %v", err)
			}
			got, err := roundTripJSON(result.StructuredContent)
			if err != nil {
				t.Fatalf("round-trip structured content: %v", err)
			}
			if diff := cmp.Diff(want, got); diff != "" {
				t.Fatalf("unexpected %s output (-want +got):\n%s", tc.tool, diff)
			}
		})
	}
}

// roundTripJSON normalises a value into generic JSON types so comparisons do
// not depend on Go struct identity or field ordering.
func roundTripJSON(v any) (any, error) {
	encoded, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, err
	}
	return decoded, nil
}
