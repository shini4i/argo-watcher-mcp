package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/shini4i/argo-watcher-mcp/internal/clock"
	"github.com/shini4i/argo-watcher-mcp/internal/domain"
)

type stubDeploymentService struct {
	capturedFilter domain.DeploymentFilter
	result         []domain.Deployment
	err            error
}

func (s *stubDeploymentService) ListDeployments(_ context.Context, filter domain.DeploymentFilter) ([]domain.Deployment, error) {
	s.capturedFilter = filter
	return s.result, s.err
}

func TestGetDeploymentsHandlerDefaults(t *testing.T) {
	now := time.Date(2024, time.January, 31, 12, 0, 0, 0, time.UTC)
	fakeClock := clock.FixedClock{At: now}
	fakeService := &stubDeploymentService{
		result: []domain.Deployment{
			{ID: "task-1"},
		},
	}

	handler := &getDeploymentsHandler{
		clock: fakeClock,
		svc:   fakeService,
	}

	_, out, err := handler.Handle(context.Background(), nil, getDeploymentsInput{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	deployments, ok := out.([]domain.Deployment)
	if !ok {
		t.Fatalf("expected []domain.Deployment, got %T", out)
	}
	if len(deployments) != 1 || deployments[0].ID != "task-1" {
		t.Fatalf("unexpected deployments result: %#v", deployments)
	}

	wantTo := now.Unix()
	wantFrom := wantTo - 30*24*60*60

	if fakeService.capturedFilter.FromTimestamp != wantFrom {
		t.Fatalf("expected from_timestamp %d, got %d", wantFrom, fakeService.capturedFilter.FromTimestamp)
	}
	if fakeService.capturedFilter.ToTimestamp != wantTo {
		t.Fatalf("expected to_timestamp %d, got %d", wantTo, fakeService.capturedFilter.ToTimestamp)
	}
}

func TestGetDeploymentsHandlerCustomTimestamps(t *testing.T) {
	fakeService := &stubDeploymentService{}
	handler := &getDeploymentsHandler{
		svc: fakeService,
	}

	from := int64(1700000000)
	to := int64(1700100000)
	input := getDeploymentsInput{
		App:      ptrOf("api"),
		FromUnix: &from,
		ToUnix:   &to,
	}

	_, _, err := handler.Handle(context.Background(), nil, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if fakeService.capturedFilter.App == nil || *fakeService.capturedFilter.App != "api" {
		t.Fatalf("expected app filter 'api', got %#v", fakeService.capturedFilter.App)
	}
	if fakeService.capturedFilter.FromTimestamp != from {
		t.Fatalf("expected from timestamp %d, got %d", from, fakeService.capturedFilter.FromTimestamp)
	}
	if fakeService.capturedFilter.ToTimestamp != to {
		t.Fatalf("expected to timestamp %d, got %d", to, fakeService.capturedFilter.ToTimestamp)
	}
}

func TestGetDeploymentsHandlerValidations(t *testing.T) {
	handler := &getDeploymentsHandler{}

	negativeDays := -1
	if _, _, err := handler.Handle(context.Background(), nil, getDeploymentsInput{DaysHistory: &negativeDays}); err == nil {
		t.Fatalf("expected error for negative day history")
	}

	from := int64(20)
	to := int64(10)
	if _, _, err := handler.Handle(context.Background(), nil, getDeploymentsInput{FromUnix: &from, ToUnix: &to}); err == nil {
		t.Fatalf("expected error when from > to")
	}
}

func TestGetDeploymentsHandlerServiceError(t *testing.T) {
	wantErr := fmt.Errorf("boom")
	fakeService := &stubDeploymentService{
		err: wantErr,
	}

	handler := &getDeploymentsHandler{
		clock: clock.FixedClock{At: time.Unix(100, 0)},
		svc:   fakeService,
	}

	_, _, err := handler.Handle(context.Background(), nil, getDeploymentsInput{})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected error %v, got %v", wantErr, err)
	}
}

func TestGetDeploymentsHandlerDaysHistoryOverride(t *testing.T) {
	now := time.Unix(200, 0)
	fakeClock := clock.FixedClock{At: now}
	fakeService := &stubDeploymentService{}

	handler := &getDeploymentsHandler{
		clock: fakeClock,
		svc:   fakeService,
	}

	days := 5
	if _, _, err := handler.Handle(context.Background(), nil, getDeploymentsInput{DaysHistory: &days}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantTo := now.Unix()
	wantFrom := wantTo - int64(days)*24*60*60

	if fakeService.capturedFilter.FromTimestamp != wantFrom {
		t.Fatalf("expected from timestamp %d, got %d", wantFrom, fakeService.capturedFilter.FromTimestamp)
	}
	if fakeService.capturedFilter.ToTimestamp != wantTo {
		t.Fatalf("expected to timestamp %d, got %d", wantTo, fakeService.capturedFilter.ToTimestamp)
	}
}
