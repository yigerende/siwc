package pluginrpc

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
)

type panickingForwardStream struct {
	grpc.ServerStream
}

func (panickingForwardStream) Context() context.Context { return context.Background() }

func (panickingForwardStream) Recv() (*pluginv1.ForwardRequest, error) { panic("boom") }

func (panickingForwardStream) Send(*pluginv1.ForwardResponse) error { return nil }

func TestForwardPanicFailsOnlyThatCall(t *testing.T) {
	panicLogPath = filepath.Join(t.TempDir(), "panics.log")
	server := New(nil)

	err := server.Forward(panickingForwardStream{})

	if status.Code(err) != codes.Internal {
		t.Fatalf("want Internal error, got %v", err)
	}
	if got := server.panics.total.Load(); got != 1 {
		t.Fatalf("panics_total = %d, want 1", got)
	}
	last := server.panics.lastRecord()
	if last == nil || last.Method != "Forward" || last.Value != "boom" {
		t.Fatalf("last panic = %+v", last)
	}
	logged, readErr := os.ReadFile(panicLogPath)
	if readErr != nil {
		t.Fatalf("read panic log: %v", readErr)
	}
	if !strings.Contains(string(logged), "panic in Forward: boom") || !strings.Contains(string(logged), "goroutine") {
		t.Fatalf("panic log lacks message or stack: %q", logged)
	}

	health, healthErr := server.Health(context.Background(), &pluginv1.HealthRequest{})
	if healthErr != nil {
		t.Fatalf("health: %v", healthErr)
	}
	if !strings.Contains(health.GetStatusJson(), `"panics_total":1`) || !strings.Contains(health.GetStatusJson(), `"method":"Forward"`) {
		t.Fatalf("health does not report the panic: %s", health.GetStatusJson())
	}
}
