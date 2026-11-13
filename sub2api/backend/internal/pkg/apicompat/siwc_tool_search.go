package apicompat

// SIWC-only adaptation, invoked after a positively identified upstream rejection.
import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

func AdaptSIWCToolSearch(body []byte) ([]byte, bool, error) {
	var req map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&req); err != nil {
		return body, false, err
	}
	tools, _ := req["tools"].([]any)
	input, _ := req["input"].([]any)
	declared := hasResponsesToolSearchDeclaration(tools)
	hasHistory := false
	for _, v := range input {
		if m, ok := v.(map[string]any); ok && (m["type"] == "tool_search_call" || m["type"] == "tool_search_output") {
			hasHistory = true
		}
	}
	if !declared && !hasHistory {
		return body, false, nil
	}
	for _, v := range tools {
		if m, ok := v.(map[string]any); ok && m["type"] != "tool_search" && m["name"] == toolSearchProxyName {
			return body, false, fmt.Errorf("proxy tool name collision")
		}
	}
	// Search-only adaptation: never recursively rewrite user data or custom calls.
	if !declared {
		tools = append(tools, map[string]any{"type": "tool_search"})
		req["tools"] = tools
	}
	for _, v := range input {
		m, ok := v.(map[string]any)
		if !ok || m["type"] != "tool_search_output" {
			continue
		}
		if _, ok := m["tools"]; !ok {
			value := m["output"]
			if s, ok := value.(string); ok {
				var decoded any
				if json.Unmarshal([]byte(s), &decoded) == nil {
					value = decoded
				}
			}
			if obj, ok := value.(map[string]any); ok {
				value = obj["tools"]
			}
			if list, ok := value.([]any); ok {
				m["tools"] = list
			}
		}
	}
	if _, err := promoteResponsesToolSearchDiscoveries(req); err != nil {
		return body, false, err
	}
	tools, _ = req["tools"].([]any)
	lowered := []any{}
	seen := false
	for _, v := range tools {
		m, ok := v.(map[string]any)
		if !ok {
			lowered = append(lowered, v)
			continue
		}
		if m["type"] == "tool_search" {
			if !seen {
				lowered = append(lowered, map[string]any{"type": "function", "name": toolSearchProxyName, "description": "Search and load client tools for the current task.", "parameters": json.RawMessage(toolSearchProxySchema)})
			}
			seen = true
		} else {
			if m["name"] == toolSearchProxyName {
				return body, false, fmt.Errorf("discovered proxy name collision")
			}
			lowered = append(lowered, m)
		}
	}
	var stripDeferred func([]any)
	stripDeferred = func(ts []any) {
		for _, v := range ts {
			if m, ok := v.(map[string]any); ok {
				delete(m, "defer_loading")
				if nested, ok := m["tools"].([]any); ok {
					stripDeferred(nested)
				}
			}
		}
	}
	stripDeferred(lowered)
	req["tools"] = lowered
	calls := map[string]bool{}
	outputs := map[string]bool{}
	for _, v := range input {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := m["type"].(string)
		if typ != "tool_search_call" && typ != "tool_search_output" {
			continue
		}
		id, _ := m["call_id"].(string)
		if strings.TrimSpace(id) == "" {
			return body, false, fmt.Errorf("missing search call_id")
		}
		if typ == "tool_search_call" {
			if calls[id] {
				return body, false, fmt.Errorf("duplicate search call_id")
			}
			calls[id] = true
			args := rawObjectString(m["arguments"])
			var parsed map[string]any
			if json.Unmarshal([]byte(args), &parsed) != nil || parsed == nil {
				return body, false, fmt.Errorf("invalid search arguments")
			}
			m["type"] = "function_call"
			m["name"] = toolSearchProxyName
			m["arguments"] = args
			delete(m, "execution")
			normalizeLoweredFunctionItemID(m)
		} else {
			if outputs[id] {
				return body, false, fmt.Errorf("duplicate search output")
			}
			outputs[id] = true
			if !calls[id] {
				continue // SIWC accepts standalone search outputs; preserve them.
			}
			m["type"] = "function_call_output"
			normalizeLoweredFunctionItemID(m)
			if err := normalizeToolSearchOutput(m); err != nil {
				return body, false, err
			}
		}
	}
	rewriteClientToolChoice(req, &ResponsesClientToolMapping{ToolSearch: true})
	out, err := json.Marshal(req)
	return out, true, err
}
