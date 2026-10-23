package dto

import "testing"

func TestSIWCWebTokenIsRedacted(t *testing.T) {
	clean, _ := RedactCredentials(map[string]any{"auth_mode": "siwc", "siwc_web_access_token": "private-token", "siwc_app_id": "app"})
	if clean["siwc_web_access_token"] != nil {
		t.Fatal("web token leaked to account DTO")
	}
	if clean["siwc_app_id"] != "app" {
		t.Fatal("nonsecret application identity lost")
	}
}
