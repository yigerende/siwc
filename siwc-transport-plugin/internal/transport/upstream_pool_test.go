package transport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"siwc-transport-plugin/internal/pluginconfig"
)

func countingClientFactory(created *int) func() (*http.Client, error) {
	return func() (*http.Client, error) {
		*created++
		return &http.Client{Transport: &http.Transport{}}, nil
	}
}

func TestUpstreamPools_AccountsNeverShareAConnection(t *testing.T) {
	var pools upstreamPools
	created := 0
	first, releaseFirst, _ := pools.acquire(upstreamPoolKey{accountID: 1}, 16, countingClientFactory(&created))
	second, releaseSecond, _ := pools.acquire(upstreamPoolKey{accountID: 2}, 16, countingClientFactory(&created))
	defer releaseFirst()
	defer releaseSecond()
	if first == second || created != 2 {
		t.Fatalf("accounts must get their own connection: same=%v created=%d", first == second, created)
	}
}

func TestUpstreamPools_OpensNewConnectionWhenFullAndRefillsOldOnes(t *testing.T) {
	var pools upstreamPools
	key := upstreamPoolKey{accountID: 7}
	created := 0
	first, releaseA, _ := pools.acquire(key, 2, countingClientFactory(&created))
	again, releaseB, _ := pools.acquire(key, 2, countingClientFactory(&created))
	third, releaseC, _ := pools.acquire(key, 2, countingClientFactory(&created))
	if again != first || third == first || created != 2 {
		t.Fatalf("a full connection must spill into a new one: again=%v third_new=%v created=%d", again == first, third != first, created)
	}
	releaseA()
	refill, releaseD, _ := pools.acquire(key, 2, countingClientFactory(&created))
	if refill != first || created != 2 {
		t.Fatalf("freed capacity on the first connection must be reused: reused=%v created=%d", refill == first, created)
	}
	releaseB()
	releaseC()
	releaseD()
	stats := pools.stats()
	if stats.Connections != 2 || stats.InFlight != 0 {
		t.Fatalf("unexpected stats after release: %+v", stats)
	}
}

func TestUpstreamPools_ReleaseIsIdempotent(t *testing.T) {
	var pools upstreamPools
	created := 0
	_, release, _ := pools.acquire(upstreamPoolKey{accountID: 1}, 16, countingClientFactory(&created))
	release()
	release()
	if stats := pools.stats(); stats.InFlight != 0 {
		t.Fatalf("in-flight must not go negative: %+v", stats)
	}
}

func TestUpstreamPools_SweepClosesIdleConnectionsOnly(t *testing.T) {
	var pools upstreamPools
	created := 0
	idleKey := upstreamPoolKey{accountID: 1}
	busyKey := upstreamPoolKey{accountID: 2}
	_, releaseIdle, _ := pools.acquire(idleKey, 16, countingClientFactory(&created))
	releaseIdle()
	_, releaseBusy, _ := pools.acquire(busyKey, 16, countingClientFactory(&created))
	defer releaseBusy()

	past := time.Now().Add(-upstreamPoolIdleTTL - time.Minute)
	pools.mu.Lock()
	for _, pool := range pools.pools {
		pool.lastUsed = past
		for _, lane := range pool.lanes {
			lane.lastUsed = past
		}
	}
	pools.sweepLocked(time.Now())
	_, idleExists := pools.pools[idleKey]
	busy := pools.pools[busyKey]
	pools.mu.Unlock()

	if idleExists {
		t.Fatal("an idle account pool must be forgotten")
	}
	if busy == nil || len(busy.lanes) != 1 {
		t.Fatal("a connection with in-flight requests must survive the sweep")
	}
}

func TestUpstreamPools_ResetStartsFreshConnections(t *testing.T) {
	var pools upstreamPools
	created := 0
	key := upstreamPoolKey{accountID: 3}
	old, releaseOld, _ := pools.acquire(key, 16, countingClientFactory(&created))
	pools.reset()
	fresh, releaseFresh, _ := pools.acquire(key, 16, countingClientFactory(&created))
	releaseOld()
	releaseFresh()
	if fresh == old || created != 2 {
		t.Fatalf("reset must open new connections: same=%v created=%d", fresh == old, created)
	}
	if stats := pools.stats(); stats.Connections != 1 || stats.InFlight != 0 {
		t.Fatalf("old connections must not be counted after reset: %+v", stats)
	}
}

func TestForwarder_ExecuteHoldsTheStreamSlotUntilBodyClose(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = io.WriteString(writer, "data: ok\n\n")
	}))
	defer upstream.Close()

	config := pluginconfig.Default()
	config.MaxStreamsPerConnection = 1
	forwarder := NewForwarder(newFakeDirectory(), config)
	plan := &Plan{AccountID: 1, Method: http.MethodPost, TargetURL: upstream.URL, Headers: http.Header{}}

	first, err := forwarder.Execute(context.Background(), plan, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	second, err := forwarder.Execute(context.Background(), plan, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	other, err := forwarder.Execute(context.Background(), &Plan{AccountID: 2, Method: http.MethodPost, TargetURL: upstream.URL, Headers: http.Header{}}, nil)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	stats := forwarder.UpstreamStats()
	if stats.Accounts != 2 || stats.Connections != 3 || stats.InFlight != 3 {
		t.Fatalf("each open stream must hold its slot per account: %+v", stats)
	}
	for _, response := range []*http.Response{first, second, other} {
		_, _ = io.ReadAll(response.Body)
		_ = response.Body.Close()
	}
	if stats := forwarder.UpstreamStats(); stats.InFlight != 0 {
		t.Fatalf("closing the bodies must release the slots: %+v", stats)
	}
}

func TestForwarder_ExecuteReleasesTheSlotOnTransportError(t *testing.T) {
	unreachable := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	target := unreachable.URL
	unreachable.Close()

	forwarder := NewForwarder(newFakeDirectory(), pluginconfig.Default())
	if _, err := forwarder.Execute(context.Background(), &Plan{AccountID: 1, Method: http.MethodPost, TargetURL: target, Headers: http.Header{}}, nil); err == nil {
		t.Fatal("expected a transport error")
	}
	if stats := forwarder.UpstreamStats(); stats.InFlight != 0 {
		t.Fatalf("a failed request must release its slot: %+v", stats)
	}
}
