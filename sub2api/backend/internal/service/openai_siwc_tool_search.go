package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

type siwcToolSearchRetryKey struct{}

const siwcToolSearchBudgetKey = "siwc_tool_search_retry_budget"

type siwcToolSearchRetryState struct {
	used     atomic.Bool
	canRetry func() bool
}

type siwcToolSearchWSTurn struct {
	turn  int
	state *siwcToolSearchRetryState
}

func bindSIWCToolSearchWSTurn(ctx context.Context, c *gin.Context, turn int) context.Context {
	const key = "siwc_tool_search_ws_turn"
	var previous *siwcToolSearchWSTurn
	if c != nil {
		if value, ok := c.Get(key); ok {
			previous, _ = value.(*siwcToolSearchWSTurn)
		}
	}
	if previous == nil || previous.turn != turn {
		previous = &siwcToolSearchWSTurn{turn: turn, state: &siwcToolSearchRetryState{canRetry: func() bool { return ctx.Err() == nil }}}
		if c != nil {
			c.Set(key, previous)
		}
	}
	return context.WithValue(ctx, siwcToolSearchRetryKey{}, previous.state)
}

// Share one budget across account attempts, while retaining the canonical body
// for every existing retry path. This helper is a no-op for ordinary OAuth.
func bindSIWCToolSearchRetry(req *http.Request, c *gin.Context, account *Account) *http.Request {
	if !account.IsOpenAISIWC() || req.Context().Value(siwcToolSearchRetryKey{}) != nil || c == nil {
		return req
	}
	var state *siwcToolSearchRetryState
	if value, ok := c.Get(siwcToolSearchBudgetKey); ok {
		state, _ = value.(*siwcToolSearchRetryState)
	}
	if state == nil {
		state = &siwcToolSearchRetryState{canRetry: func() bool {
			return c.Request != nil && c.Request.Context().Err() == nil && c.Writer != nil && !c.Writer.Written()
		}}
		c.Set(siwcToolSearchBudgetKey, state)
	}
	return req.WithContext(context.WithValue(req.Context(), siwcToolSearchRetryKey{}, state))
}

func hasSIWCToolSearchCall(body []byte) bool {
	for _, item := range gjson.GetBytes(body, "input").Array() {
		if item.Get("type").String() == "tool_search_call" {
			return true
		}
	}
	return false
}

func isSIWCToolSearchInputError(status int, body []byte) bool {
	return status == http.StatusBadRequest && gjson.GetBytes(body, "error.code").String() == "subscription_sharing_unsupported_capability" && gjson.GetBytes(body, "error.param").String() == "input"
}

type siwcReplayBody struct {
	io.Reader
	io.Closer
}
type siwcRoundTrip func(*http.Request) (*http.Response, error)

// Execute before any response is returned to the caller. The first request is
// untouched. A positively identified rejection permits one same-account retry;
// no partial success, transport error, 429/502, or unrelated 400 is replayed.
func (s *OpenAIGatewayService) doSIWCToolSearchRetry(request *http.Request, account *Account, roundTrip siwcRoundTrip) (*http.Response, error) {
	response, err := roundTrip(request)
	state, _ := request.Context().Value(siwcToolSearchRetryKey{}).(*siwcToolSearchRetryState)
	if err != nil || response == nil || response.StatusCode != http.StatusBadRequest || response.Body == nil || !account.IsOpenAISIWC() || state == nil || state.used.Load() || state.canRetry == nil || !state.canRetry() || request.Context().Err() != nil || request.GetBody == nil || s == nil || s.settingService == nil {
		return response, err
	}
	enabled, _ := s.settingService.GetSIWCToolSearchCompatibility(request.Context())
	if !enabled {
		return response, err
	}
	// Bounded inspection; unmatched errors retain their original body and Close.
	source := response.Body
	evidence, readErr := io.ReadAll(io.LimitReader(source, 64*1024+1))
	response.Body = &siwcReplayBody{Reader: io.MultiReader(bytes.NewReader(evidence), source), Closer: source}
	if readErr != nil || len(evidence) > 64*1024 || !isSIWCToolSearchInputError(response.StatusCode, evidence) {
		return response, err
	}
	reader, readErr := request.GetBody()
	if readErr != nil {
		return response, err
	}
	body, readErr := io.ReadAll(reader)
	_ = reader.Close()
	if readErr != nil || !hasSIWCToolSearchCall(body) {
		return response, err
	}
	converted, changed, adaptErr := apicompat.AdaptSIWCToolSearch(body)
	if adaptErr != nil || !changed {
		return response, err
	}
	if request.Context().Err() != nil || !state.canRetry() || !state.used.CompareAndSwap(false, true) {
		return response, err
	}
	next := request.Clone(request.Context())
	next.Body = io.NopCloser(bytes.NewReader(converted))
	next.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(converted)), nil }
	next.ContentLength = int64(len(converted))
	next.Header = request.Header.Clone()
	next.Header.Del("Content-Length")
	_ = response.Body.Close()
	slog.InfoContext(request.Context(), "siwc_tool_search_compat_retry", "account_id", account.ID, "attempt", 1)
	retried, retryErr := roundTrip(next)
	if retryErr == nil && retried != nil && retried.StatusCode >= 200 && retried.StatusCode < 300 && retried.Body != nil {
		// Plugin RPC preserves wire header casing (HTTP/2 uses lowercase).
		// Normalize only this adapted response so both the restorer and existing
		// downstream SSE/JSON handlers recognize it, including non-stream clients.
		headers := make(http.Header, len(retried.Header))
		for name, values := range retried.Header {
			if strings.EqualFold(name, "Content-Length") {
				continue
			}
			if strings.EqualFold(name, "Content-Type") {
				name = "Content-Type"
			}
			headers[name] = append(headers[name], values...)
		}
		retried.Header = headers
		// Some transports omit or mislabel Content-Type while returning SSE.
		// Peek only the framing prefix and replay it unchanged; do not buffer
		// the response or trust the client's stream flag (SIWC can force SSE).
		reader := bufio.NewReader(retried.Body)
		prefix, _ := reader.Peek(6)
		stream := isEventStreamResponse(headers) || bytes.HasPrefix(prefix, []byte("event:")) ||
			bytes.HasPrefix(prefix, []byte("data:")) || bytes.HasPrefix(prefix, []byte(":")) ||
			bytes.HasPrefix(prefix, []byte("id:")) || bytes.HasPrefix(prefix, []byte("retry:"))
		if stream {
			headers.Set("Content-Type", "text/event-stream")
		}
		retried.Body = newSIWCToolSearchBody(&siwcReplayBody{Reader: reader, Closer: retried.Body}, stream)
		retried.ContentLength = -1
		slog.InfoContext(request.Context(), "siwc_tool_search_compat_retry_accepted", "account_id", account.ID)
	}
	return retried, retryErr
}

// Pull-based streaming transformation: no background writer, no full-response
// buffering, and Close/cancellation propagate to the original upstream body.
type siwcToolSearchBody struct {
	source   io.ReadCloser
	scanner  *bufio.Scanner
	restorer *apicompat.SIWCToolSearchRestorer
	pending  *bytes.Reader
	stream   bool
	done     bool
}

func newSIWCToolSearchBody(source io.ReadCloser, stream bool) io.ReadCloser {
	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	return &siwcToolSearchBody{source: source, scanner: scanner, restorer: apicompat.NewSIWCToolSearchRestorer(), stream: stream}
}
func (r *siwcToolSearchBody) Close() error { return r.source.Close() }
func (r *siwcToolSearchBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if r.pending != nil && r.pending.Len() > 0 {
			return r.pending.Read(p)
		}
		if r.done {
			return 0, io.EOF
		}
		if !r.stream {
			data, err := io.ReadAll(io.LimitReader(r.source, 16*1024*1024+1))
			if err != nil {
				return 0, err
			}
			r.done = true
			if len(data) > 16*1024*1024 {
				return 0, errors.New("SIWC tool search response exceeds limit")
			}
			out, err := apicompat.RestoreSIWCToolSearchPayload(data)
			if err != nil {
				return 0, err
			}
			r.pending = bytes.NewReader(out)
			continue
		}
		var lines [][]byte
		eventBytes := 0
		for r.scanner.Scan() {
			line := bytes.Clone(r.scanner.Bytes())
			if len(line) == 0 {
				if len(lines) == 0 {
					continue
				}
				break
			}
			lines = append(lines, line)
			eventBytes += len(line)
			if len(lines) > 32768 || eventBytes > 16*1024*1024 {
				return 0, errors.New("SIWC tool search event exceeds line limit")
			}
		}
		if err := r.scanner.Err(); err != nil {
			r.done = true
			return 0, err
		}
		if len(lines) == 0 {
			r.done = true
			return 0, io.EOF
		}
		var data []byte
		for _, line := range lines {
			if bytes.HasPrefix(line, []byte("data:")) {
				if len(data) > 0 {
					data = append(data, '\n')
				}
				data = append(data, bytes.TrimPrefix(bytes.TrimPrefix(line, []byte("data:")), []byte(" "))...)
			}
		}
		if len(data) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("[DONE]")) {
			r.pending = bytes.NewReader(append(bytes.Join(lines, []byte("\n")), []byte("\n\n")...))
			continue
		}
		rows, err := r.restorer.RestoreEvent(data)
		if err != nil {
			r.done = true
			return 0, err
		}
		var output bytes.Buffer
		for _, row := range rows {
			// Retain SSE event/id/retry fields, updating the event name to its restored type.
			for _, line := range lines {
				if bytes.HasPrefix(line, []byte("data:")) {
					continue
				}
				if bytes.HasPrefix(line, []byte("event:")) {
					output.WriteString("event: " + gjson.GetBytes(row, "type").String() + "\n")
				} else {
					output.Write(line)
					output.WriteByte('\n')
				}
			}
			output.WriteString("data: ")
			output.Write(row)
			output.WriteString("\n\n")
		}
		r.pending = bytes.NewReader(output.Bytes())
	}
}
