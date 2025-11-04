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
	expected := []domain.Deployment{
		{
			ID:        "deployment-1",
			App:       "demo",
			Author:    "alice",
			Project:   "kube",
			Images:    []domain.Image{{Image: "registry/demo", Tag: "v1"}},
			Status:    "Success",
			Created:   now,
			Updated:   now,
			Validated: true,
		},
	}

	service := &stubDeploymentService{
		result: expected,
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

	var got []domain.Deployment
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal structured content: %v", err)
	}

	if diff := cmp.Diff(expected, got); diff != "" {
		t.Fatalf("unexpected tool output (-want +got):\n%s", diff)
	}
}
