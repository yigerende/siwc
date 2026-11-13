package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type siwcRemoteCompactionContextKey struct{}

// Snapshot the toggle once, before scheduling. No account credentials or
// persisted capability flags are changed; ordinary OAuth never uses the adapter.
func (s *OpenAIGatewayService) WithSIWCRemoteCompactionCompatibility(ctx context.Context) context.Context {
	if s == nil || s.settingService == nil {
		return ctx
	}
	enabled, _ := s.settingService.GetSIWCRemoteCompactionCompatibility(ctx)
	if !enabled {
		return ctx
	}
	return context.WithValue(ctx, siwcRemoteCompactionContextKey{}, true)
}

func siwcRemoteCompactionAllowed(ctx context.Context) bool {
	return ctx != nil && ctx.Value(siwcRemoteCompactionContextKey{}) == true
}

// Only native streaming trigger requests are adapted. Existing automatic
// compaction requests and legacy /compact keep their existing paths.
func prepareSIWCRemoteCompactionRequest(request *http.Request) (*http.Request, bool, error) {
	if request.GetBody == nil || strings.HasSuffix(strings.TrimRight(request.URL.Path, "/"), "/compact") {
		return request, false, nil
	}
	reader, err := request.GetBody()
	if err != nil {
		return nil, false, err
	}
	body, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		return nil, false, err
	}
	if !gjson.GetBytes(body, "stream").Bool() || !HasCompactionTriggerInInput(body) {
		return request, false, nil
	}
	kept := make([]json.RawMessage, 0)
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() != "compaction_trigger" {
			kept = append(kept, json.RawMessage(item.Raw))
		}
	}
	input, err := json.Marshal(kept)
	if err != nil {
		return nil, false, err
	}
	body, err = sjson.SetRawBytes(body, "input", input)
	if err != nil {
		return nil, false, err
	}
	management := make([]json.RawMessage, 0)
	for _, item := range gjson.GetBytes(body, "context_management").Array() {
		if item.Get("type").String() != "compaction" {
			management = append(management, json.RawMessage(item.Raw))
		}
	}
	// 1000 is the upstream minimum (verified against the real SIWC route).
	// Short histories may not compact; the response adapter reports that as a
	// failure, never fabricating an encrypted item or padding user history.
	management = append(management, json.RawMessage(`{"type":"compaction","compact_threshold":1000}`))
	cm, _ := json.Marshal(management)
	body, err = sjson.SetRawBytes(body, "context_management", cm)
	if err != nil {
		return nil, false, err
	}
	body, err = sjson.SetBytes(body, "store", false)
	if err != nil {
		return nil, false, err
	}
	// This is a maintenance turn: tool definitions/history remain intact for
	// context, but no server-side tools should execute after compaction.
	if len(gjson.GetBytes(body, "tools").Array()) > 0 {
		body, err = sjson.SetBytes(body, "tool_choice", "none")
		if err != nil {
			return nil, false, err
		}
	}
	next := request.Clone(request.Context())
	next.Body = io.NopCloser(bytes.NewReader(body))
	next.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	next.ContentLength = int64(len(body))
	next.Header = request.Header.Clone()
	next.Header.Del("Content-Length")
	return next, true, nil
}

func (s *OpenAIGatewayService) doSIWCRemoteCompaction(request *http.Request, proxyURL string, account *Account) (*http.Response, error) {
	next, adapted, err := prepareSIWCRemoteCompactionRequest(request)
	if err != nil {
		return nil, err
	}
	if adapted && request.Body != nil {
		_ = request.Body.Close()
	}
	if s.pluginManager != nil {
		response, handled, transportErr := s.pluginManager.RoundTripOpenAIOAuth(next.Context(), next, proxyURL, account)
		if handled {
			if transportErr == nil && adapted && response != nil && response.StatusCode >= 200 && response.StatusCode < 300 {
				response.Body = newSIWCRemoteCompactionBody(response.Body)
				response.ContentLength = -1
				response.Header.Del("Content-Length")
				response.Header.Set("Content-Type", "text/event-stream")
			}
			return response, transportErr
		}
	}
	// The adapter is never allowed to send a SIWC credential down ordinary OAuth.
	return nil, errors.New("SIWC remote compaction transport is unavailable")
}

// Pull-based bounded SSE adaptation: no background goroutine, no full history
// buffer, and Close immediately closes upstream. Inference output/tool calls
// never reach the compact client. We require one encrypted item AND completion.
type siwcRemoteCompactionBody struct {
	source     io.ReadCloser
	scanner    *bufio.Scanner
	pending    *bytes.Reader
	compaction json.RawMessage
	responseID string
	createdAt  int64
	terminal   bool
	total      int
}

func newSIWCRemoteCompactionBody(source io.ReadCloser) io.ReadCloser {
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	return &siwcRemoteCompactionBody{source: source, scanner: scanner, createdAt: time.Now().Unix()}
}

func (r *siwcRemoteCompactionBody) Close() error { return r.source.Close() }

func (r *siwcRemoteCompactionBody) failure(code, message string, response []byte) []byte {
	r.terminal = true
	if !gjson.ValidBytes(response) {
		response = []byte("{}")
	}
	id := gjson.GetBytes(response, "id").String()
	if id == "" {
		id = r.responseID
	}
	if id == "" {
		id = "resp_siwc_compaction_failed"
	}
	fields := map[string]any{"id": id, "object": "response", "created_at": r.createdAt, "status": "failed", "output": []any{}, "error": map[string]string{"type": "server_error", "code": code, "message": message}}
	if code == "siwc_compaction_missing" || code == "siwc_compaction_invalid_result" {
		fields["error"] = map[string]string{"type": "invalid_request_error", "code": code, "message": message}
	}
	if code == "siwc_compaction_upstream_failed" {
		if original := gjson.GetBytes(response, "error"); original.IsObject() {
			fields["error"] = json.RawMessage(original.Raw)
		}
	}
	if usage := gjson.GetBytes(response, "usage"); usage.IsObject() {
		fields["usage"] = json.RawMessage(usage.Raw)
	}
	data, _ := json.Marshal(map[string]any{"type": "response.failed", "sequence_number": 0, "response": fields})
	return append(append([]byte("data: "), data...), []byte("\n\n")...)
}

func (r *siwcRemoteCompactionBody) event() []byte {
	for r.scanner.Scan() {
		line := r.scanner.Bytes()
		r.total += len(line)
		if r.total > 64*1024*1024 {
			return r.failure("siwc_compaction_response_too_large", "SIWC compaction response exceeded the safety limit", nil)
		}
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[5:])
		if bytes.Equal(data, []byte("[DONE]")) {
			return r.failure("siwc_compaction_incomplete", "SIWC compaction ended without response.completed", nil)
		}
		if !gjson.ValidBytes(data) {
			return r.failure("siwc_compaction_invalid_response", "Invalid SIWC compaction response", nil)
		}
		event := gjson.ParseBytes(data)
		kind := event.Get("type").String()
		if id := event.Get("response.id").String(); id != "" {
			r.responseID = id
		}
		if created := event.Get("response.created_at").Int(); created > 0 {
			r.createdAt = created
		}
		if kind == "response.output_item.done" && event.Get("item.type").String() == "compaction" {
			item := event.Get("item")
			if item.Get("encrypted_content").Type != gjson.String || item.Get("encrypted_content").String() == "" {
				return r.failure("siwc_compaction_invalid_result", "Expected an encrypted SIWC compaction item", nil)
			}
			r.compaction = append(json.RawMessage(nil), item.Raw...)
		}
		switch kind {
		case "response.completed":
			res := event.Get("response")
			items := make([]json.RawMessage, 0, 1)
			for _, item := range res.Get("output").Array() {
				if item.Get("type").String() == "compaction" {
					if item.Get("encrypted_content").Type != gjson.String || item.Get("encrypted_content").String() == "" {
						return r.failure("siwc_compaction_invalid_result", "SIWC returned an empty compaction item", []byte(res.Raw))
					}
					// Automatic compaction can run more than once in one response.
					// The latest encrypted state supersedes earlier prefixes.
					items = []json.RawMessage{json.RawMessage(item.Raw)}
				}
			}
			if len(items) == 0 && r.compaction != nil {
				items = append(items, r.compaction)
			}
			if len(items) != 1 {
				return r.failure("siwc_compaction_missing", "SIWC did not produce exactly one encrypted compaction item; original history must be retained", []byte(res.Raw))
			}
			if r.compaction != nil && !bytes.Equal(bytes.TrimSpace(r.compaction), bytes.TrimSpace(items[0])) {
				var a, b any
				_ = json.Unmarshal(r.compaction, &a)
				_ = json.Unmarshal(items[0], &b)
				aa, _ := json.Marshal(a)
				bb, _ := json.Marshal(b)
				if !bytes.Equal(aa, bb) {
					return r.failure("siwc_compaction_invalid_result", "SIWC compaction output changed before completion", []byte(res.Raw))
				}
			}
			output, _ := json.Marshal(items)
			final, err := sjson.SetRawBytes([]byte(res.Raw), "output", output)
			if err != nil {
				return r.failure("siwc_compaction_invalid_response", "Invalid SIWC compaction completion", nil)
			}
			final, _ = sjson.SetBytes(final, "created_at", r.createdAt)
			payload, ok := buildOpenAICompactSSEPayload(final)
			if !ok {
				return r.failure("siwc_compaction_invalid_response", "Invalid SIWC compaction completion", nil)
			}
			r.terminal = true
			return payload
		case "response.failed", "response.incomplete", "error":
			msg := extractOpenAISSEErrorMessage(data)
			if msg == "" {
				msg = "SIWC compaction failed before completion"
			}
			response := []byte(event.Get("response").Raw)
			if kind == "error" {
				response = data
			}
			return r.failure("siwc_compaction_upstream_failed", msg, response)
		}
		// Keep the downstream reader alive without exposing inference output.
		return []byte(": siwc compaction pending\n\n")
	}
	if err := r.scanner.Err(); err != nil {
		return r.failure("siwc_compaction_stream_error", fmt.Sprintf("SIWC compaction stream interrupted: %s", sanitizeUpstreamErrorMessage(err.Error())), nil)
	}
	return r.failure("siwc_compaction_incomplete", "SIWC compaction stream ended before completion", nil)
}

func (r *siwcRemoteCompactionBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if r.pending != nil && r.pending.Len() > 0 {
			return r.pending.Read(p)
		}
		if r.terminal {
			return 0, io.EOF
		}
		r.pending = bytes.NewReader(r.event())
	}
}
