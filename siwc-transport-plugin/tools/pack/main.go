// pack 把插件工程打包成 .s2plugin（sub2api 插件包格式 v1）。
//
// 用法（在插件工程根目录执行）：
//
//	go run ./tools/pack [-output dist/local.siwc.openai.transport.s2plugin]
//	                    [-targets windows-amd64,linux-amd64,darwin-arm64]
//	                    [-signing-key build/keys/publisher.private] [-key-id my-key-v1]
//
// 步骤：按目标交叉编译运行时二进制 → 计算 manifest.files 哈希（运行时 + UI）
// → 生成 manifest.json（manifest.source.json 的补全版）→ 打成 ZIP →
// （可选）用 Ed25519 私钥对 manifest.json 精确字节签名写 signature.json。
package main

import (
	"archive/zip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type manifestFile struct {
	SchemaVersion int                          `json:"schema_version"`
	ID            string                       `json:"id"`
	Name          string                       `json:"name"`
	Version       string                       `json:"version"`
	Description   string                       `json:"description,omitempty"`
	Author        string                       `json:"author,omitempty"`
	Requires      map[string]any               `json:"requires"`
	Capabilities  []map[string]string          `json:"capabilities"`
	Runtimes      map[string]map[string]string `json:"runtimes"`
	UI            map[string]string            `json:"ui"`
	Files         map[string]string            `json:"files"`
}

type signatureFile struct {
	Algorithm string `json:"algorithm"`
	KeyID     string `json:"key_id"`
	Signature string `json:"signature"`
}

func main() {
	defaultOutput := filepath.Join("dist", "local.siwc.openai.transport.s2plugin")
	output := flag.String("output", defaultOutput, "输出 .s2plugin 路径")
	targets := flag.String("targets", "windows-amd64,linux-amd64,darwin-amd64,darwin-arm64", "逗号分隔的 GOOS-GOARCH 目标列表")
	signingKey := flag.String("signing-key", "", "Ed25519 私钥文件（Base64 文本）。留空则打未签名包")
	keyID := flag.String("key-id", "", "签名 key_id，与宿主 trusted_publishers 配置键一致")
	flag.Parse()

	if err := run(*output, strings.Split(*targets, ","), *signingKey, *keyID); err != nil {
		fmt.Fprintln(os.Stderr, "pack:", err)
		os.Exit(1)
	}
}

func run(output string, targets []string, signingKeyPath, keyID string) error {
	source, err := os.ReadFile("manifest.source.json")
	if err != nil {
		return fmt.Errorf("read manifest.source.json: %w", err)
	}
	var manifest manifestFile
	if err := json.Unmarshal(source, &manifest); err != nil {
		return fmt.Errorf("parse manifest.source.json: %w", err)
	}

	files := make(map[string]string)
	runtimes := make(map[string]map[string]string)

	// binaryNameFor：Windows 目标带 .exe，其余平台用裸名。
	binaryNameFor := func(goos string) string {
		if goos == "windows" {
			return "siwc-transport.exe"
		}
		return "siwc-transport"
	}

	// 1. 交叉编译运行时。
	if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp("", "s2plugin-pack-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()

	for _, target := range targets {
		target = strings.TrimSpace(target)
		if target == "" {
			continue
		}
		parts := strings.SplitN(target, "-", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid target %q (want GOOS-GOARCH)", target)
		}
		runtimePath := filepath.ToSlash(filepath.Join("runtimes", target, binaryNameFor(parts[0])))
		localPath := filepath.Join(staging, strings.ReplaceAll(runtimePath, "/", string(os.PathSeparator)))
		if err := os.MkdirAll(filepath.Dir(localPath), 0o755); err != nil {
			return err
		}
		if err := buildBinary(parts[0], parts[1], localPath); err != nil {
			return fmt.Errorf("build %s: %w", target, err)
		}
		hash, size, err := hashFile(localPath)
		if err != nil {
			return err
		}
		if size == 0 {
			return fmt.Errorf("runtime for %s is empty", target)
		}
		files[runtimePath] = hash
		runtimes[target] = map[string]string{"path": runtimePath}
	}
	if len(runtimes) == 0 {
		return fmt.Errorf("no targets built")
	}

	// 2. UI 文件。
	uiFiles, err := collectFiles("ui")
	if err != nil {
		return fmt.Errorf("collect ui: %w", err)
	}
	for path, hash := range uiFiles {
		files[path] = hash
	}

	// 3. 补全 manifest.json（requires / capabilities / ui / id 等来自 source）。
	manifest.Runtimes = runtimes
	manifest.Files = files
	if manifest.UI == nil {
		manifest.UI = map[string]string{}
	}
	if _, ok := manifest.UI["entrypoint"]; !ok {
		manifest.UI["entrypoint"] = "ui/index.html"
	}
	manifestBytes, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	manifestBytes = append(manifestBytes, '\n')

	// 4. 打 ZIP。
	zipFile, err := os.Create(output)
	if err != nil {
		return err
	}
	defer func() { _ = zipFile.Close() }()

	writer := zip.NewWriter(zipFile)
	if err := addBytes(writer, "manifest.json", manifestBytes); err != nil {
		return err
	}
	for path, hash := range files {
		data, err := readStagedOrWorkspace(staging, path)
		if err != nil {
			return fmt.Errorf("stage %s: %w", path, err)
		}
		digest := sha256.Sum256(data)
		if hex.EncodeToString(digest[:]) != hash {
			return fmt.Errorf("hash mismatch while staging %s", path)
		}
		if err := addBytes(writer, path, data); err != nil {
			return err
		}
	}

	// 5. 可选签名（对象是 manifest.json 的精确字节）。
	if signingKeyPath != "" {
		if keyID == "" {
			return fmt.Errorf("-key-id is required when signing")
		}
		keyRaw, err := os.ReadFile(signingKeyPath)
		if err != nil {
			return fmt.Errorf("read signing key: %w", err)
		}
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(keyRaw)))
		if err != nil || len(key) != ed25519.PrivateKeySize {
			return fmt.Errorf("signing key must be base64 ed25519 private key (use tools/keygen)")
		}
		signature := ed25519.Sign(ed25519.PrivateKey(key), manifestBytes)
		signatureBytes, err := json.MarshalIndent(signatureFile{
			Algorithm: "ed25519",
			KeyID:     keyID,
			Signature: base64.StdEncoding.EncodeToString(signature),
		}, "", "  ")
		if err != nil {
			return err
		}
		if err := addBytes(writer, "signature.json", append(signatureBytes, '\n')); err != nil {
			return err
		}
	}

	if err := writer.Close(); err != nil {
		return err
	}
	fmt.Printf("已生成 %s（%d 个运行时，%d 个文件，签名: %v）\n", output, len(runtimes), len(files), signingKeyPath != "")
	return nil
}

func buildBinary(goos, goarch, outputPath string) error {
	cmd := exec.Command("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", outputPath, "./cmd/siwc-transport")
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func hashFile(path string) (string, int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), int64(len(data)), nil
}

func collectFiles(root string) (map[string]string, error) {
	out := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		hash, _, err := hashFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(path)] = hash
		return nil
	})
	return out, err
}

// readStagedOrWorkspace：运行时二进制来自 staging；UI 文件来自工作区。
func readStagedOrWorkspace(staging, logical string) ([]byte, error) {
	staged := filepath.Join(staging, filepath.FromSlash(logical))
	if data, err := os.ReadFile(staged); err == nil {
		return data, nil
	}
	return os.ReadFile(filepath.FromSlash(logical))
}

func addBytes(writer *zip.Writer, name string, data []byte) error {
	entry, err := writer.Create(name)
	if err != nil {
		return err
	}
	_, err = entry.Write(data)
	return err
}

// sortedKeys 用于稳定输出（诊断辅助）。
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

var _ = sortedKeys
