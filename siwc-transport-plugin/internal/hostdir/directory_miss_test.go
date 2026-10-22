package hostdir

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
)

type listAccountsHost struct {
	pluginv1.HostServiceClient
	calls atomic.Int32
}

func (host *listAccountsHost) ListAccounts(context.Context, *pluginv1.ListAccountsRequest, ...grpc.CallOption) (*pluginv1.ListAccountsResponse, error) {
	host.calls.Add(1)
	return &pluginv1.ListAccountsResponse{Accounts: []*pluginv1.AccountInfo{
		{Id: 1, Status: "active", Schedulable: true, MetadataJson: []byte(`{"extra":{"auth_provider":"siwc"}}`)},
		{Id: 2, Status: "active", Schedulable: true, MetadataJson: []byte(`{"extra":{"auth_provider":"siwc"}}`)},
	}}, nil
}

// A cache miss inside the TTL used to unlock the mutex twice, a fatal error
// that terminated the plugin with every in-flight stream.
func TestAccountCacheMissWithinTTLRefreshesConcurrently(t *testing.T) {
	host := &listAccountsHost{}
	directory := New(time.Minute)
	directory.AttachClient(host, HostServiceAPIVersionMin)
	if _, ok := directory.Account(context.Background(), 1); !ok {
		t.Fatal("account 1 not found after the first refresh")
	}
	directory.mu.Lock()
	delete(directory.cached, 2)
	directory.mu.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			meta, ok := directory.Account(context.Background(), 2)
			if !ok || !meta.SIWC {
				t.Errorf("account 2 = %+v, %v; want SIWC account after the miss refresh", meta, ok)
			}
		}()
	}
	wg.Wait()
	if host.calls.Load() < 2 {
		t.Fatalf("ListAccounts calls = %d; the miss must force a refresh", host.calls.Load())
	}
}
