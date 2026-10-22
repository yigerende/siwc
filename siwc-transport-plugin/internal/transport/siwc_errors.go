package transport

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// RelayResponse is the response the plugin relays to the host.
type RelayResponse struct {
	StatusCode    int
	Status        string
	Headers       http.Header
	ContentLength int64
	Body          io.Reader
}

// PrepareRelayResponse returns the upstream response as relayed to the host.
// For SIWC error responses the small error body is buffered so a SIWC-specific
// error can be translated before the status line is committed; the second
// result reports whether a translation happened.
func PrepareRelayResponse(siwc bool, response *http.Response) (RelayResponse, bool) {
	relay := RelayResponse{
		StatusCode:    response.StatusCode,
		Status:        response.Status,
		Headers:       response.Header,
		ContentLength: response.ContentLength,
		Body:          response.Body,
	}
	if !siwc || response.StatusCode < http.StatusBadRequest {
		return relay, false
	}
	peeked, peekErr := io.ReadAll(io.LimitReader(response.Body, SIWCErrorPeekLimit))
	relay.Body = io.MultiReader(bytes.NewReader(peeked), response.Body)
	if peekErr != nil || len(peeked) >= SIWCErrorPeekLimit {
		return relay, false
	}
	mapped, rewritten, ok := NormalizeSIWCErrorResponse(response.StatusCode, peeked)
	if !ok {
		return relay, false
	}
	relay.StatusCode = mapped
	relay.Status = fmt.Sprintf("%d %s", mapped, http.StatusText(mapped))
	relay.Headers = response.Header.Clone()
	relay.Headers.Del("Content-Length")
	relay.ContentLength = int64(len(rewritten))
	relay.Body = bytes.NewReader(rewritten)
	return relay, true
}

// SIWCErrorPeekLimit bounds how much of an upstream error body is buffered to
// read its error code before the response is relayed to the host.
const SIWCErrorPeekLimit = 64 << 10

// siwcUsageLimitCooldownSeconds is advertised as resets_in_seconds on a
// translated usage-limit error. The SIWC route never states a reset time, so
// this matches the host's bounded SIWC cooldown instead of inventing one.
const siwcUsageLimitCooldownSeconds = 1800

const siwcUsageLimitErrorCode = "subscription_sharing_usage_limit_exceeded"

// NormalizeSIWCErrorResponse translates a SIWC usage-limit error into the
// Codex backend shape that the host already treats as quota exhaustion:
// HTTP 429 with error.type usage_limit_reached and a reset hint. The route
// answers this code with HTTP 400 invalid_request_error, which the host would
// otherwise relay to the client instead of cooling the account down and
// switching to another one. The original code and message are kept.
func NormalizeSIWCErrorResponse(statusCode int, body []byte) (int, []byte, bool) {
	if statusCode < http.StatusBadRequest {
		return statusCode, body, false
	}
	if strings.TrimSpace(gjson.GetBytes(body, "error.code").String()) != siwcUsageLimitErrorCode {
		return statusCode, body, false
	}
	out := body
	if next, err := sjson.SetBytes(out, "error.type", "usage_limit_reached"); err == nil {
		out = next
	}
	if !gjson.GetBytes(out, "error.resets_in_seconds").Exists() && !gjson.GetBytes(out, "error.resets_at").Exists() {
		if next, err := sjson.SetBytes(out, "error.resets_in_seconds", siwcUsageLimitCooldownSeconds); err == nil {
			out = next
		}
	}
	return http.StatusTooManyRequests, out, true
}
