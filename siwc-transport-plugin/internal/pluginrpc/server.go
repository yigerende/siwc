// Package pluginrpc implements the sub2api TransportPlugin v1 server:
// GetInfo / Health / ValidateConfig / ApplyConfig / TestConfig / Forward /
// InitHostServices.
package pluginrpc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"

	hcplugin "github.com/hashicorp/go-plugin"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"

	"siwc-transport-plugin/internal/hostdir"
	"siwc-transport-plugin/internal/pluginconfig"
	"siwc-transport-plugin/internal/transport"
)

const (
	pluginID      = "local.siwc.openai.transport"
	pluginVersion = "0.2.5"

	capabilityOpenAI  = "openai.oauth.outbound_transport.v1"
	responseChunkSize = 32 * 1024
)

// Server implements TransportPlugin. SetHostBroker is invoked by the go-plugin
// GRPCPlugin wrapper when the plugin process registers its services.
type Server struct {
	pluginv1.UnimplementedTransportPluginServer

	directory  *hostdir.Directory
	forwarder  *transport.Forwarder
	appliedCfg atomic.Pointer[pluginconfig.Config]
	panics     panicTracker

	broker atomic.Pointer[hcplugin.GRPCBroker]
}

// New builds the server. directory may be nil (host did not provide the KV /
// account-directory host service): the plugin then degrades to pure
// transparent forwarding and reports that honestly.
func New(directory *hostdir.Directory) *Server {
	defaults := pluginconfig.Default()
	server := &Server{directory: directory}
	server.forwarder = transport.NewForwarder(directory, defaults)
	server.appliedCfg.Store(&defaults)
	return server
}

// SetHostBroker implements pluginv1.HostBrokerReceiver.
func (server *Server) SetHostBroker(broker *hcplugin.GRPCBroker) {
	server.broker.Store(broker)
}

// InitHostServices dials back the host HostService and wires the directory.
func (server *Server) InitHostServices(ctx context.Context, request *pluginv1.InitHostServicesRequest) (*pluginv1.InitHostServicesResponse, error) {
	if server.directory == nil {
		return &pluginv1.InitHostServicesResponse{Ready: false, Message: "directory not configured"}, nil
	}
	broker := server.broker.Load()
	if broker == nil {
		return &pluginv1.InitHostServicesResponse{Ready: false, Message: "host broker not injected"}, nil
	}
	if request.GetHostServiceApiVersion() < hostdir.HostServiceAPIVersionMin {
		return &pluginv1.InitHostServicesResponse{
			Ready:   false,
			Message: fmt.Sprintf("host service api version %d < required %d", request.GetHostServiceApiVersion(), hostdir.HostServiceAPIVersionMin),
		}, nil
	}
	conn, err := broker.Dial(request.GetHostServiceId())
	if err != nil {
		return &pluginv1.InitHostServicesResponse{Ready: false, Message: fmt.Sprintf("dial host service: %v", err)}, nil
	}
	server.directory.AttachConn(conn, request.GetHostServiceApiVersion())
	return &pluginv1.InitHostServicesResponse{Ready: true}, nil
}

func (server *Server) GetInfo(ctx context.Context, request *pluginv1.GetInfoRequest) (*pluginv1.GetInfoResponse, error) {
	return &pluginv1.GetInfoResponse{
		PluginId:            pluginID,
		PluginVersion:       pluginVersion,
		ProtocolVersion:     pluginv1.ProtocolVersion,
		TransportApiVersion: pluginv1.TransportAPIVersion,
		Capabilities:        []string{capabilityOpenAI},
	}, nil
}

func (server *Server) Health(ctx context.Context, request *pluginv1.HealthRequest) (response *pluginv1.HealthResponse, err error) {
	defer server.recoverRPC("Health", &err)
	status := map[string]any{
		"plugin_version":       pluginVersion,
		"host_service_ready":   server.directory != nil && server.directory.Available(),
		"cached_accounts":      0,
		"requests_total":       server.forwarder.RequestsTotal.Load(),
		"siwc_requests":        server.forwarder.SIWCRequests.Load(),
		"passthrough_requests": server.forwarder.PassthroughReqs.Load(),
		"errors_total":         server.forwarder.ErrorsTotal.Load(),
		"in_flight":            server.forwarder.InFlight.Load(),

		"siwc_responses_requests":      server.forwarder.SIWCResponsesRequests.Load(),
		"prompt_cache_key_from_client": server.forwarder.PromptCacheKeyFromClient.Load(),
		"prompt_cache_key_synthesized": server.forwarder.PromptCacheKeySynthesized.Load(),
		"tool_search_stripped":         server.forwarder.ToolSearchStripped.Load(),
		"error_rewrites":               server.forwarder.ErrorRewrites.Load(),
		"recent_prompt_cache_keys":     server.forwarder.RecentCacheKeys(),
		"upstream":                     server.forwarder.UpstreamStats(),
		"panics_total":                 server.panics.total.Load(),
		"last_panic":                   server.panics.lastRecord(),
	}
	config := server.appliedCfg.Load()
	if config != nil {
		status["config"] = map[string]any{
			"direct_base_url":            config.DirectBaseURL,
			"force_store_false":          config.ForceStoreFalse,
			"force_stream":               config.ForceStream,
			"rewrite_chatgpt_codex_urls": config.RewriteChatGPTCodexURLs,
			"max_streams_per_connection": config.MaxStreamsPerConnection,
		}
	}
	if server.directory != nil {
		status["cached_accounts"] = server.directory.CachedCount()
		if err := server.directory.LastRefreshError(); err != nil {
			status["directory_last_error"] = err.Error()
		}
		status["host_service_api_version"] = server.directory.HostAPIVersion()
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		return &pluginv1.HealthResponse{Healthy: true, Message: "ok", StatusJson: "{}"}, nil
	}
	return &pluginv1.HealthResponse{Healthy: true, Message: "ok", StatusJson: string(encoded)}, nil
}

func (server *Server) ValidateConfig(ctx context.Context, request *pluginv1.ValidateConfigRequest) (*pluginv1.ValidateConfigResponse, error) {
	_, normalized, err := pluginconfig.Validate(request.GetConfigJson())
	if err != nil {
		return &pluginv1.ValidateConfigResponse{Valid: false, Message: err.Error()}, nil
	}
	return &pluginv1.ValidateConfigResponse{
		Valid:                true,
		Message:              "ok",
		NormalizedConfigJson: normalized,
	}, nil
}

func (server *Server) ApplyConfig(ctx context.Context, request *pluginv1.ApplyConfigRequest) (response *pluginv1.ApplyConfigResponse, err error) {
	defer server.recoverRPC("ApplyConfig", &err)
	config, _, err := pluginconfig.Validate(request.GetConfigJson())
	if err != nil {
		return &pluginv1.ApplyConfigResponse{Applied: false, Message: err.Error()}, nil
	}
	if err := server.forwarder.ApplyConfig(config); err != nil {
		return &pluginv1.ApplyConfigResponse{Applied: false, Message: err.Error()}, nil
	}
	server.appliedCfg.Store(&config)
	return &pluginv1.ApplyConfigResponse{Applied: true, Message: "ok"}, nil
}

// TestConfig is a passive diagnosis: parse the saved config and summarize the
// runtime status without touching any upstream (the host gates test behind
// step-up 2FA).
func (server *Server) TestConfig(ctx context.Context, request *pluginv1.TestConfigRequest) (*pluginv1.TestConfigResponse, error) {
	if _, _, err := pluginconfig.Validate(request.GetConfigJson()); err != nil {
		return &pluginv1.TestConfigResponse{Success: false, Message: err.Error()}, nil
	}
	health, _ := server.Health(ctx, &pluginv1.HealthRequest{})
	message := "config valid (passive diagnosis, no upstream call)"
	if server.directory != nil && !server.directory.Available() {
		message = "config valid; host HostService not ready (SIWC detection degrades to passthrough)"
	}
	return &pluginv1.TestConfigResponse{
		Success:    true,
		Message:    message,
		LatencyMs:  0,
		StatusJson: health.GetStatusJson(),
	}, nil
}

// Forward is the transport contract core. Frame order:
//
//	request start -> 0..n body_chunk -> body_end; response start -> 0..n
//	body_chunk -> end; failures produce an error frame. request_sent is false
//	only while the upstream HTTP transport has not been attempted.
func (server *Server) Forward(stream pluginv1.TransportPlugin_ForwardServer) (err error) {
	defer server.recoverRPC("Forward", &err)
	ctx := stream.Context()

	first, err := stream.Recv()
	if err != nil {
		return err
	}
	start := first.GetStart()
	if start == nil {
		return sendForwardError(stream, "PLUGIN_PROTOCOL", "first frame must be start", false)
	}

	requestBody, err := readForwardBody(stream, start, server.forwarder.BodyLimit())
	if err != nil {
		return sendForwardError(stream, "PLUGIN_PROTOCOL", err.Error(), false)
	}

	server.forwarder.RequestsTotal.Add(1)
	server.forwarder.InFlight.Add(1)
	defer server.forwarder.InFlight.Add(-1)

	plan, err := server.forwarder.Decide(ctx, start.GetMethod(), start.GetUrl(),
		http.Header(headersFromProto(start.GetHeaders())), requestBody,
		start.GetProxyUrl(), start.GetAccountId())
	if err != nil {
		server.forwarder.ErrorsTotal.Add(1)
		return sendForwardError(stream, "PLUGIN_DECISION_FAILED", err.Error(), false)
	}
	if plan.SIWC {
		server.forwarder.SIWCRequests.Add(1)
	} else {
		server.forwarder.PassthroughReqs.Add(1)
	}

	// CRITICAL: use plan.Body (the REWRITTEN body after strip/store/stream),
	// NOT the original requestBody — this was the bug that made every body
	// rewrite (strip agent_message/computer_call, force store/stream) a no-op.
	var bodyReader io.Reader
	if len(plan.Body) > 0 {
		bodyReader = bytes.NewReader(plan.Body)
	}
	response, err := server.forwarder.Execute(ctx, plan, bodyReader)
	if err != nil {
		server.forwarder.ErrorsTotal.Add(1)
		return sendForwardError(stream, "PLUGIN_UPSTREAM_FAILED", err.Error(), true)
	}
	defer func() { _ = response.Body.Close() }()

	relay, rewritten := transport.PrepareRelayResponse(plan.SIWC, response)
	if rewritten {
		server.forwarder.ErrorRewrites.Add(1)
	}

	if err := stream.Send(&pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_Start{Start: &pluginv1.ForwardResponseStart{
		StatusCode:    int32(relay.StatusCode),
		Status:        relay.Status,
		Protocol:      response.Proto,
		ProtocolMajor: int32(response.ProtoMajor),
		ProtocolMinor: int32(response.ProtoMinor),
		Headers:       headersToProto(relay.Headers),
		ContentLength: relay.ContentLength,
	}}}); err != nil {
		return err
	}

	buffer := make([]byte, responseChunkSize)
	var received int64
	for {
		n, readErr := relay.Body.Read(buffer)
		if n > 0 {
			received += int64(n)
			if err := stream.Send(&pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_BodyChunk{BodyChunk: buffer[:n]}}); err != nil {
				return err
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			// Mid-stream upstream failure: the upstream did receive the
			// request; report it as request_sent=true per the contract.
			server.forwarder.ErrorsTotal.Add(1)
			return sendForwardError(stream, "PLUGIN_UPSTREAM_STREAM_FAILED", readErr.Error(), true)
		}
	}

	return stream.Send(&pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_End{End: &pluginv1.ForwardResponseEnd{
		BytesReceived: received,
		DurationMs:    0,
	}}})
}

func readForwardBody(stream pluginv1.TransportPlugin_ForwardServer, start *pluginv1.ForwardRequestStart, limit int64) ([]byte, error) {
	if !start.GetHasBody() {
		// Still consume up to body_end to keep the frame order intact.
		for {
			frame, err := stream.Recv()
			if err != nil {
				return nil, err
			}
			if frame.GetBodyEnd() {
				return nil, nil
			}
			if frame.GetStart() != nil {
				return nil, errors.New("unexpected second start frame")
			}
		}
	}
	var body bytes.Buffer
	for {
		frame, err := stream.Recv()
		if err != nil {
			return nil, err
		}
		if chunk := frame.GetBodyChunk(); chunk != nil {
			if int64(body.Len())+int64(len(chunk)) > limit {
				return nil, fmt.Errorf("request body exceeds plugin buffer limit (%d bytes)", limit)
			}
			body.Write(chunk)
			continue
		}
		if frame.GetBodyEnd() {
			return body.Bytes(), nil
		}
		if frame.GetStart() != nil {
			return nil, errors.New("unexpected second start frame")
		}
		return nil, errors.New("unexpected frame while reading request body")
	}
}

func sendForwardError(stream pluginv1.TransportPlugin_ForwardServer, code, message string, requestSent bool) error {
	return stream.Send(&pluginv1.ForwardResponse{Frame: &pluginv1.ForwardResponse_Error{Error: &pluginv1.ForwardResponseError{
		Code:        code,
		Message:     message,
		RequestSent: requestSent,
	}}})
}

func headersFromProto(source map[string]*pluginv1.HeaderValues) map[string][]string {
	headers := make(map[string][]string, len(source))
	for name, values := range source {
		if values == nil {
			continue
		}
		headers[name] = append([]string(nil), values.GetValues()...)
	}
	return headers
}

func headersToProto(source map[string][]string) map[string]*pluginv1.HeaderValues {
	headers := make(map[string]*pluginv1.HeaderValues, len(source))
	for name, values := range source {
		headers[name] = &pluginv1.HeaderValues{Values: append([]string(nil), values...)}
	}
	return headers
}
