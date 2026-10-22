package pluginconfig

import (
	"encoding/json"
	"testing"
)

func TestValidate_EmptyObjectBecomesDefaults(t *testing.T) {
	config, encoded, err := Validate([]byte(`{}`))
	if err != nil {
		t.Fatalf("Validate({}) failed: %v", err)
	}
	if config.DirectBaseURL != "https://api.openai.com" {
		t.Fatalf("direct_base_url default = %q", config.DirectBaseURL)
	}
	if !config.ForceStoreFalse || !config.ForceStream || !config.RewriteChatGPTCodexURLs {
		t.Fatalf("SIWC 强制规则默认必须开启: %+v", config)
	}
	if config.MaxBufferedBodyBytes != 256<<20 {
		t.Fatalf("max_buffered_body_bytes default = %d, want the host's 256 MiB gateway limit", config.MaxBufferedBodyBytes)
	}
	if config.MaxStreamsPerConnection != 16 {
		t.Fatalf("max_streams_per_connection default = %d, want 16", config.MaxStreamsPerConnection)
	}
	var roundTrip map[string]any
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("normalized config is not valid JSON: %v", err)
	}
	if _, ok := roundTrip["direct_base_url"]; !ok {
		t.Fatalf("normalized config missing direct_base_url")
	}
}

func TestValidate_RejectsUnknownFields(t *testing.T) {
	if _, _, err := Validate([]byte(`{"force_streams": true}`)); err == nil {
		t.Fatal("unknown field must be rejected")
	}
}

func TestValidate_RejectsInsecureBaseURL(t *testing.T) {
	if _, _, err := Validate([]byte(`{"direct_base_url": "http://api.openai.com"}`)); err == nil {
		t.Fatal("http base url must be rejected")
	}
	if _, _, err := Validate([]byte(`{"direct_base_url": "https://gateway.example.com/base"}`)); err != nil {
		t.Fatalf("https base url with path should be accepted: %v", err)
	}
}

func TestValidate_RangeSemantics(t *testing.T) {
	// 未设置/非正值回落默认值。
	config, _, err := Validate([]byte(`{"dial_timeout_seconds": -5}`))
	if err != nil {
		t.Fatalf("unset/negative values should fall back to defaults: %v", err)
	}
	if config.DialTimeoutSeconds != defaultDialTimeoutSeconds {
		t.Fatalf("dial timeout default = %d", config.DialTimeoutSeconds)
	}
	// 超过上限的显式值直接拒绝（防止静默钳制掩盖配置错误）。
	if _, _, err := Validate([]byte(`{"account_directory_ttl_seconds": 99999}`)); err == nil {
		t.Fatal("ttl above ceiling must be rejected")
	}
	if _, _, err := Validate([]byte(`{"max_buffered_body_bytes": 999999999999}`)); err == nil {
		t.Fatal("body limit above ceiling must be rejected")
	}
	if _, _, err := Validate([]byte(`{"max_streams_per_connection": 101}`)); err == nil {
		t.Fatal("streams per connection above ceiling must be rejected")
	}
	if config, _, err := Validate([]byte(`{"max_streams_per_connection": 0}`)); err != nil || config.MaxStreamsPerConnection != 16 {
		t.Fatalf("unset streams per connection must fall back to 16: %+v %v", config.MaxStreamsPerConnection, err)
	}
}
