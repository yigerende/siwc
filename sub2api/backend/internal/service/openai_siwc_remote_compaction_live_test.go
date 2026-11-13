package service

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Opt-in bridge for real-client acceptance. It has no DB, workers or customer
// files. The upstream is an SSH loopback tunnel injecting a test administrator
// key in server memory. Encrypted context and credentials are never logged.
func TestSIWCRemoteCompactionLiveBridge(t *testing.T) {
	upstream := os.Getenv("SIWC_COMPACTION_LIVE_UPSTREAM")
	if upstream == "" {
		t.Skip("opt-in real upstream test")
	}
	var enabled atomic.Bool
	enabled.Store(true)
	done := make(chan struct{}, 1)
	client := &http.Client{Timeout: 120 * time.Second}
	mux := http.NewServeMux()
	mux.HandleFunc("/test/toggle", func(w http.ResponseWriter, r *http.Request) {
		enabled.Store(r.URL.Query().Get("enabled") == "true")
		_ = json.NewEncoder(w).Encode(map[string]bool{"enabled": enabled.Load()})
	})
	mux.HandleFunc("/test/stop", func(w http.ResponseWriter, r *http.Request) {
		select {
		case done <- struct{}{}:
		default:
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 16*1024*1024+1))
		if err != nil || len(body) > 16*1024*1024 {
			http.Error(w, "request too large", 413)
			return
		}
		target := strings.TrimRight(upstream, "/") + r.URL.RequestURI()
		req, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
		if err != nil {
			http.Error(w, "invalid test request", 400)
			return
		}
		req.Header = r.Header.Clone()
		adapted := false
		if enabled.Load() {
			req, adapted, err = prepareSIWCRemoteCompactionRequest(req)
			if err != nil {
				http.Error(w, "adapter error", 400)
				return
			}
		}
		begin := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "test upstream transport failure", 502)
			t.Log("transport failure")
			return
		}
		defer resp.Body.Close()
		if adapted && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			resp.Body = newSIWCRemoteCompactionBody(resp.Body)
		}
		for k, vs := range resp.Header {
			if strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Transfer-Encoding") {
				continue
			}
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		buffer := make([]byte, 32*1024)
		for {
			n, err := resp.Body.Read(buffer)
			if n > 0 {
				if _, e := w.Write(buffer[:n]); e != nil {
					break
				}
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			if err != nil {
				break
			}
		}
		t.Logf("request path=%s adapted=%v http=%d elapsed_ms=%d", r.URL.Path, adapted, resp.StatusCode, time.Since(begin).Milliseconds())
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	ready := os.Getenv("SIWC_COMPACTION_LIVE_READY")
	if ready == "" {
		t.Fatal("ready file required")
	}
	if err := os.WriteFile(ready, []byte(server.URL), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log("isolated client bridge ready")
	select {
	case <-done:
	case <-time.After(15 * time.Minute):
		t.Fatal("live test timed out")
	}
}
