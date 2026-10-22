package pluginrpc

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// panicLogPath receives recovered handler panics. The host discards plugin
// stdout/stderr, so without this file a panic leaves no trace.
var panicLogPath = filepath.Join(os.TempDir(), "siwc-transport-panics.log")

const panicStackLimit = 16 << 10

// PanicRecord summarizes the latest recovered panic for Health.
type PanicRecord struct {
	At     time.Time `json:"at"`
	Method string    `json:"method"`
	Value  string    `json:"value"`
}

type panicTracker struct {
	total atomic.Int64
	mu    sync.Mutex
	last  *PanicRecord
}

func (tracker *panicTracker) record(method string, value any, stack []byte) {
	tracker.total.Add(1)
	entry := PanicRecord{At: time.Now(), Method: method, Value: fmt.Sprint(value)}
	tracker.mu.Lock()
	tracker.last = &entry
	tracker.mu.Unlock()

	if len(stack) > panicStackLimit {
		stack = stack[:panicStackLimit]
	}
	file, err := os.OpenFile(panicLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	_, _ = fmt.Fprintf(file, "%s panic in %s: %s\n%s\n", entry.At.Format(time.RFC3339Nano), method, entry.Value, stack)
}

func (tracker *panicTracker) lastRecord() *PanicRecord {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if tracker.last == nil {
		return nil
	}
	record := *tracker.last
	return &record
}

// recoverRPC must be deferred directly by an RPC handler. It turns a panic into
// an Internal error for that call: an unrecovered panic would terminate the
// plugin process and drop every stream it is serving.
func (server *Server) recoverRPC(method string, err *error) {
	recovered := recover()
	if recovered == nil {
		return
	}
	server.panics.record(method, recovered, debug.Stack())
	*err = status.Errorf(codes.Internal, "plugin internal error in %s", method)
}
