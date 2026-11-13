package apicompat

import (
	"encoding/json"
	"github.com/tidwall/gjson"
)

// SIWCToolSearchRestorer never lowers native custom/namespace tools. Its state
// belongs to a single response, including for simultaneous requests.
type SIWCToolSearchRestorer struct {
	stream      *ResponsesClientToolStreamRestorer
	nativeItems map[string]bool
}

func NewSIWCToolSearchRestorer() *SIWCToolSearchRestorer {
	return &SIWCToolSearchRestorer{stream: NewResponsesClientToolStreamRestorer(ResponsesClientToolMapping{ToolSearch: true}), nativeItems: make(map[string]bool)}
}

func RestoreSIWCToolSearchPayload(body []byte) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, err
	}
	target := root
	if raw, ok := root["response"]; ok {
		target = nil
		if err := json.Unmarshal(raw, &target); err != nil {
			return nil, err
		}
	}
	var items []json.RawMessage
	raw, ok := target["output"]
	if !ok {
		return body, nil
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, err
	}
	changed := false
	for i, item := range items {
		v := gjson.ParseBytes(item)
		if v.Get("type").String() != "function_call" || v.Get("name").String() != toolSearchProxyName || v.Get("namespace").String() != "" {
			continue
		}
		restored, yes, err := RestoreResponsesClientToolPayload(item, ResponsesClientToolMapping{ToolSearch: true})
		if err != nil {
			return nil, err
		}
		if yes {
			items[i] = restored
			changed = true
		}
	}
	if !changed {
		return body, nil
	}
	target["output"], _ = json.Marshal(items)
	if _, ok := root["response"]; ok {
		root["response"], _ = json.Marshal(target)
	}
	return json.Marshal(root)
}

func (r *SIWCToolSearchRestorer) RestoreEvent(body []byte) ([][]byte, error) {
	v := gjson.ParseBytes(body)
	if isResponsesClientToolTerminalEvent(v.Get("type").String()) {
		out, err := RestoreSIWCToolSearchPayload(body)
		if err != nil {
			return nil, err
		}
		rows, _, err := r.stream.resequenceRaw(out, int(v.Get("sequence_number").Int()), false)
		return rows, err
	}
	// An unrelated native namespace may contain a function named tool_search.
	// Preserve the raw event, including fields unknown to the generic adapter.
	if v.Get("item.namespace").String() != "" {
		if id := v.Get("item.id").String(); id != "" {
			r.nativeItems[id] = true
		}
		rows, _, err := r.stream.resequenceRaw(body, int(v.Get("sequence_number").Int()), false)
		return rows, err
	}
	if r.nativeItems[v.Get("item_id").String()] {
		rows, _, err := r.stream.resequenceRaw(body, int(v.Get("sequence_number").Int()), false)
		return rows, err
	}
	rows, _, err := r.stream.RestoreEvent(body)
	return rows, err
}
