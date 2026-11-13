package service

import (
	"context"
	"encoding/json"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Apply after group reasoning mapping, immediately before the HTTP request is
// built for the SIWC transport plugin. Ordinary OAuth and disabled mode are no-ops.
func (s *OpenAIGatewayService) normalizeSIWCConfigurationUpdates(ctx context.Context, account *Account, body []byte) []byte {
	if !account.IsOpenAISIWC() || s == nil || s.settingService == nil {
		return body
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}
	items := input.Array()
	found := false
	for _, item := range items {
		if item.Get("type").String() == "configuration_update" {
			found = true
			break
		}
	}
	if !found {
		return body
	}
	enabled, _ := s.settingService.GetSIWCConfigurationUpdateCompatibility(ctx)
	if !enabled {
		return body
	}
	kept := make([]json.RawMessage, 0, len(items))
	latestEffort := ""
	for _, item := range items {
		if item.Get("type").String() == "configuration_update" {
			if effort := item.Get("reasoning.effort"); effort.Type == gjson.String && effort.String() != "" {
				latestEffort = effort.String()
			}
			continue
		}
		kept = append(kept, json.RawMessage(item.Raw))
	}
	filtered, err := json.Marshal(kept)
	if err != nil {
		return body
	}
	out, err := sjson.SetRawBytes(body, "input", filtered)
	if err != nil {
		return body
	}
	// BPS extracts effort from configuration records. Unlike BPS, do not
	// overwrite the current request/group mapping with historical configuration.
	// No reasoning value (including "none") is renamed or remapped here.
	if latestEffort != "" && !gjson.GetBytes(body, "reasoning.effort").Exists() {
		out, err = sjson.SetBytes(out, "reasoning.effort", latestEffort)
		if err != nil {
			return body
		}
	}
	return out
}
