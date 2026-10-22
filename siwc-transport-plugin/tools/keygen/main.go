// keygen 生成插件发布用 Ed25519 密钥对。
//
//	go run ./tools/keygen -out build/keys/my-publisher
//
// 生成 my-publisher.private（Base64 私钥，仅保存在受控开发机/CI Secret）
// 与 my-publisher.public（Base64 公钥，交给部署者写入 sub2api 配置：
// plugins.trusted_publishers.<key-id>: "<BASE64 公钥>"）。
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	out := flag.String("out", "build/keys/publisher", "输出前缀（生成 <out>.private 与 <out>.public）")
	flag.Parse()

	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		fmt.Fprintln(os.Stderr, "keygen:", err)
		os.Exit(1)
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "keygen:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out+".private", []byte(base64.StdEncoding.EncodeToString(privateKey)+"\n"), 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "keygen:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out+".public", []byte(base64.StdEncoding.EncodeToString(publicKey)+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "keygen:", err)
		os.Exit(1)
	}
	fmt.Printf("私钥: %s.private（保密，勿入仓库/插件包）\n公钥: %s.public（Base64，配置到 trusted_publishers）\n", *out, *out)
}
