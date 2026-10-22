package transport

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

const usageLimitBody = `{"error":{"message":"The ChatGPT user has reached their Subscription Sharing usage limit.","type":"invalid_request_error","param":null,"code":"subscription_sharing_usage_limit_exceeded"}}`

func TestNormalizeSIWCErrorResponse_UsageLimit(t *testing.T) {
	status, out, ok := NormalizeSIWCErrorResponse(http.StatusBadRequest, []byte(usageLimitBody))
	if !ok || status != http.StatusTooManyRequests {
		t.Fatalf("usage limit must become 429: status=%d ok=%v", status, ok)
	}
	if gjson.GetBytes(out, "error.type").String() != "usage_limit_reached" {
		t.Fatalf("error.type must use the Codex quota shape: %s", out)
	}
	if gjson.GetBytes(out, "error.resets_in_seconds").Int() != siwcUsageLimitCooldownSeconds {
		t.Fatalf("resets_in_seconds missing: %s", out)
	}
	if gjson.GetBytes(out, "error.code").String() != siwcUsageLimitErrorCode ||
		!strings.Contains(gjson.GetBytes(out, "error.message").String(), "Subscription Sharing usage limit") {
		t.Fatalf("original code and message must be kept: %s", out)
	}
}

func TestNormalizeSIWCErrorResponse_OtherErrorsUntouched(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"error":{"message":"tool 'tool_search' is not supported","type":"invalid_request_error","param":"tools","code":"subscription_sharing_unsupported_capability"}}`),
		[]byte(`{"detail":{"error_code":"access_denied"}}`),
		[]byte(`not json`),
	} {
		status, out, ok := NormalizeSIWCErrorResponse(http.StatusBadRequest, body)
		if ok || status != http.StatusBadRequest || string(out) != string(body) {
			t.Fatalf("unrelated error must be untouched: %s", body)
		}
	}
	if _, _, ok := NormalizeSIWCErrorResponse(http.StatusOK, []byte(usageLimitBody)); ok {
		t.Fatal("successful responses must never be rewritten")
	}
}

func newUpstreamResponse(status int, body string) *http.Response {
	header := http.Header{}
	header.Set("Content-Type", "application/json")
	header.Set("Content-Length", strconv.Itoa(len(body)))
	header.Set("X-Request-Id", "req_1")
	return &http.Response{
		StatusCode:    status,
		Status:        strconv.Itoa(status) + " " + http.StatusText(status),
		Header:        header,
		ContentLength: int64(len(body)),
		Body:          io.NopCloser(strings.NewReader(body)),
	}
}

func TestPrepareRelayResponse_TranslatesSIWCUsageLimit(t *testing.T) {
	relay, rewritten := PrepareRelayResponse(true, newUpstreamResponse(http.StatusBadRequest, usageLimitBody))
	if !rewritten || relay.StatusCode != http.StatusTooManyRequests || relay.Status != "429 Too Many Requests" {
		t.Fatalf("unexpected relay: %d %q rewritten=%v", relay.StatusCode, relay.Status, rewritten)
	}
	body, err := io.ReadAll(relay.Body)
	if err != nil {
		t.Fatalf("read relay body: %v", err)
	}
	if relay.ContentLength != int64(len(body)) || relay.Headers.Get("Content-Length") != "" {
		t.Fatalf("length must follow the rewritten body: content_length=%d header=%q len=%d",
			relay.ContentLength, relay.Headers.Get("Content-Length"), len(body))
	}
	if relay.Headers.Get("X-Request-Id") != "req_1" {
		t.Fatal("other upstream headers must be kept")
	}
	if gjson.GetBytes(body, "error.type").String() != "usage_limit_reached" {
		t.Fatalf("body not translated: %s", body)
	}
}

func TestPrepareRelayResponse_KeepsOtherResponsesByteForByte(t *testing.T) {
	toolError := `{"error":{"message":"tool 'tool_search' is not supported","code":"subscription_sharing_unsupported_capability"}}`
	cases := []struct {
		siwc   bool
		status int
		body   string
	}{
		{siwc: true, status: http.StatusBadRequest, body: toolError},
		{siwc: true, status: http.StatusOK, body: "data: {}\n\n"},
		{siwc: false, status: http.StatusBadRequest, body: usageLimitBody},
	}
	for _, testCase := range cases {
		upstream := newUpstreamResponse(testCase.status, testCase.body)
		relay, rewritten := PrepareRelayResponse(testCase.siwc, upstream)
		if rewritten || relay.StatusCode != testCase.status || relay.ContentLength != int64(len(testCase.body)) {
			t.Fatalf("response must pass through: %+v", testCase)
		}
		body, err := io.ReadAll(relay.Body)
		if err != nil || string(body) != testCase.body {
			t.Fatalf("body must pass through unchanged: %q err=%v", body, err)
		}
	}
}

func TestPrepareRelayResponse_LargeErrorBodyIsStreamedIntact(t *testing.T) {
	large := `{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"` + strings.Repeat("x", SIWCErrorPeekLimit) + `"}}`
	relay, rewritten := PrepareRelayResponse(true, newUpstreamResponse(http.StatusBadRequest, large))
	if rewritten || relay.StatusCode != http.StatusBadRequest {
		t.Fatal("bodies beyond the peek limit must not be rewritten")
	}
	body, err := io.ReadAll(relay.Body)
	if err != nil || string(body) != large {
		t.Fatalf("large body must be relayed intact: len=%d err=%v", len(body), err)
	}
}
