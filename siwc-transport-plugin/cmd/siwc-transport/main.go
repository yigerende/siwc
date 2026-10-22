// siwc-transport 是 sub2api 的 OpenAI OAuth 出站传输插件：
// SIWC (Sign in with ChatGPT) token-sharing 渠道。
//
// 进程由 sub2api 宿主按 go-plugin 协议启动；入口只负责装配并 Serve，
// 实际逻辑在 internal/pluginrpc（协议）与 internal/transport（改写与转发）。
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"

	"siwc-transport-plugin/internal/hostdir"
	"siwc-transport-plugin/internal/pluginrpc"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	keepCrashOutput()
	directory := hostdir.New(30 * time.Second)
	server := pluginrpc.New(directory)
	pluginv1.Serve(server)
}

// keepCrashOutput records each start and routes fatal errors (unrecovered
// panics, concurrent map writes, out of memory) to a file, because the host
// discards the plugin's stderr.
func keepCrashOutput() {
	file, err := os.OpenFile(filepath.Join(os.TempDir(), "siwc-transport-crash.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	_, _ = fmt.Fprintf(file, "%s start pid=%d\n", time.Now().Format(time.RFC3339Nano), os.Getpid())
	_ = debug.SetCrashOutput(file, debug.CrashOptions{})
}
