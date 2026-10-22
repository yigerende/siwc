package hostdir

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
)

// gatedHost blocks its first ListAccounts call on gates[0] and every later one
// on gates[1].
type gatedHost struct {
	pluginv1.HostServiceClient
	calls atomic.Int32
	gates [2]chan struct{}
}

func (host *gatedHost) ListAccounts(context.Context, *pluginv1.ListAccountsRequest, ...grpc.CallOption) (*pluginv1.ListAccountsResponse, error) {
	gate := host.gates[1]
	if host.calls.Add(1) == 1 {
		gate = host.gates[0]
	}
	<-gate
	return &pluginv1.ListAccountsResponse{Accounts: []*pluginv1.AccountInfo{
		{Id: 1, Status: "active", MetadataJson: []byte(`{"extra":{"auth_provider":"siwc"}}`)},
		{Id: 2, Status: "active", MetadataJson: []byte(`{"extra":{"auth_provider":"siwc"}}`)},
	}}, nil
}

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached within 5s")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestDirectoryRefreshIsSharedAndOffTheHitPath(t *testing.T) {
	host := &gatedHost{gates: [2]chan struct{}{make(chan struct{}), make(chan struct{})}}
	defer close(host.gates[1])
	directory := New(time.Hour)
	directory.AttachClient(host, HostServiceAPIVersionMin)
	ctx := context.Background()

	results := make(chan bool, 32)
	for i := 0; i < cap(results); i++ {
		go func() {
			meta, ok := directory.Account(ctx, 2)
			results <- ok && meta.SIWC
		}()
	}
	waitFor(t, func() bool { return host.calls.Load() == 1 })
	time.Sleep(20 * time.Millisecond)
	close(host.gates[0])
	for i := 0; i < cap(results); i++ {
		if !<-results {
			t.Fatal("a miss must resolve from the shared refresh")
		}
	}
	if calls := host.calls.Load(); calls != 1 {
		t.Fatalf("concurrent misses made %d ListAccounts calls, want 1", calls)
	}

	directory.mu.Lock()
	directory.cachedAt = time.Now().Add(-2 * time.Hour)
	directory.mu.Unlock()
	started := time.Now()
	for i := 0; i < 32; i++ {
		if meta, ok := directory.Account(ctx, 1); !ok || !meta.SIWC {
			t.Fatal("an expired entry must still be served")
		}
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("expired hits waited %v for the refresh", elapsed)
	}
	waitFor(t, func() bool { return host.calls.Load() == 2 })
	time.Sleep(20 * time.Millisecond)
	if calls := host.calls.Load(); calls != 2 {
		t.Fatalf("expired hits made %d ListAccounts calls in total, want one background refresh", calls)
	}
}
