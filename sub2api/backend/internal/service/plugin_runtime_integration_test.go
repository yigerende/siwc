package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pluginFailingRequestBody struct{}

func (pluginFailingRequestBody) Read([]byte) (int, error) {
	return 0, errors.New("测试请求体读取失败")
}
func (pluginFailingRequestBody) Close() error { return nil }

// 以下三个钩子让本地插件（非官方 OpenAI Transport）复用同一集成测试：
// 默认值保持官方插件的原行为。

func hostVersionForPluginIntegrationTest() string {
	if version := strings.TrimSpace(os.Getenv("SUB2API_TEST_PLUGIN_HOST_VERSION")); version != "" {
		return version
	}
	return "0.1.179"
}

func pluginIntegrationUIFiles() []string {
	if raw := strings.TrimSpace(os.Getenv("SUB2API_TEST_PLUGIN_UI_FILES")); raw != "" {
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	}
	return []string{
		"ui/index.html",
		"ui/assets/styles.css",
		"ui/assets/bridge-v1.js",
		"ui/assets/app.js",
	}
}

func pluginIntegrationConfigJSON() string {
	if configJSON := strings.TrimSpace(os.Getenv("SUB2API_TEST_PLUGIN_CONFIG")); configJSON != "" {
		return configJSON
	}
	return `{
		"request_timeout_seconds":30,
		"response_header_timeout_seconds":10,
		"idle_connection_timeout_seconds":30,
		"max_idle_connections":10,
		"max_idle_connections_per_host":5,
		"enable_http2":true,
		"tls_min_version":"1.2",
		"proxy_mode":"disabled",
		"extra_headers":{"X-Plugin-Test":"enabled"}
	}`
}

// pluginIntegrationExpectedRequestHeader 返回插件出站请求应携带的额外头
// （官方插件的 extra_headers 注入）。格式 Name:Value；空串表示无额外头断言。
func pluginIntegrationExpectedRequestHeader() (string, string) {
	raw := strings.TrimSpace(os.Getenv("SUB2API_TEST_PLUGIN_EXPECT_HEADER"))
	if raw == "" {
		return "", ""
	}
	name, value, _ := strings.Cut(raw, ":")
	return strings.TrimSpace(name), strings.TrimSpace(value)
}

func TestPluginRuntimeIntegration(t *testing.T) {
	packagePath := os.Getenv("SUB2API_TEST_PLUGIN_PACKAGE")
	if packagePath == "" {
		t.Skip("未提供 SUB2API_TEST_PLUGIN_PACKAGE，跳过本地插件进程集成测试")
	}
	packageFile, err := os.Open(packagePath)
	require.NoError(t, err)
	defer func() { _ = packageFile.Close() }()

	root := t.TempDir()
	cfg := testPluginConfig(root, false)
	// 允许本地自定义发布者密钥（默认留空 → 仅官方内置密钥可用，保持原行为）。
	if keyID := os.Getenv("SUB2API_TEST_TRUSTED_KEY_ID"); keyID != "" {
		publicKey := strings.TrimSpace(os.Getenv("SUB2API_TEST_TRUSTED_PUBLIC_KEY"))
		require.NotEmpty(t, publicKey, "提供了 SUB2API_TEST_TRUSTED_KEY_ID 时必须同时提供公钥")
		cfg.Plugins.TrustedPublishers = map[string]string{keyID: publicKey}
	}
	installer := NewPluginPackageInstaller(cfg, PluginHostInfo{Version: hostVersionForPluginIntegrationTest(), BuildType: "release"})
	installation, err := installer.Install(context.Background(), packageFile, nil)
	require.NoError(t, err)
	assert.Equal(t, PluginSignatureTrusted, installation.SignatureStatus)
	for _, relative := range pluginIntegrationUIFiles() {
		assert.FileExists(t, filepath.Join(installation.InstallPath, filepath.FromSlash(relative)))
	}

	runtime, err := startPluginRuntime(context.Background(), installation, 10*time.Second, filepath.Join(root, "runtime"), nil)
	require.NoError(t, err)
	defer runtime.kill()
	require.NoError(t, runtime.validateAndApplyConfig(context.Background(), []byte(pluginIntegrationConfigJSON())))

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, readErr := io.ReadAll(request.Body)
		require.NoError(t, readErr)
		assert.Equal(t, "payload", string(body))
		if headerName, headerValue := pluginIntegrationExpectedRequestHeader(); headerName != "" {
			assert.Equal(t, headerValue, request.Header.Get(headerName))
		}
		assert.Equal(t, int64(len("payload")), request.ContentLength)
		assert.Empty(t, request.TransferEncoding)
		writer.Header().Set("X-Upstream", "plugin-runtime")
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte("response-through-plugin"))
	}))
	defer upstream.Close()

	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, upstream.URL, bytes.NewBufferString("payload"))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1}
	require.True(t, runtime.beginRequest())
	response, err := runtime.roundTrip(context.Background(), request, "", account)
	if err != nil {
		runtime.finishRequest()
	}
	require.NoError(t, err)
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, response.StatusCode)
	assert.Equal(t, "plugin-runtime", response.Header.Get("X-Upstream"))
	assert.Equal(t, "response-through-plugin", string(body))
	assert.Equal(t, int64(len("response-through-plugin")), response.ContentLength)
	require.NoError(t, response.Body.Close())

	failingRequest, err := http.NewRequestWithContext(context.Background(), http.MethodPost, upstream.URL, pluginFailingRequestBody{})
	require.NoError(t, err)
	failingRequest.ContentLength = -1
	require.True(t, runtime.beginRequest())
	started := time.Now()
	_, err = runtime.roundTrip(context.Background(), failingRequest, "", account)
	runtime.finishRequest()
	require.Error(t, err)
	assert.Less(t, time.Since(started), 2*time.Second)
}
