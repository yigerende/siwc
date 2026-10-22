package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDrainRuntimesInBackgroundKeepsInFlightRequests(t *testing.T) {
	runtime := &pluginRuntime{done: make(chan struct{})}
	require.True(t, runtime.beginRequest())

	start := time.Now()
	drainRuntimesInBackground(runtime, nil)
	require.Less(t, time.Since(start), time.Second, "draining must not block the caller")
	require.False(t, runtime.beginRequest(), "a draining runtime must refuse new requests")

	select {
	case <-runtime.done:
		t.Fatal("drain must wait for the in-flight request")
	case <-time.After(50 * time.Millisecond):
	}

	runtime.finishRequest()
	select {
	case <-runtime.done:
	case <-time.After(time.Second):
		t.Fatal("drain must complete once the last in-flight request finishes")
	}
}
