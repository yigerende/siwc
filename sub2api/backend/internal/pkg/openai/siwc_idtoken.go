package openai

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// SIWC ID token 验签（依据 SIWC sign-in 文档第 4 步：必须对 OpenAI 公开的
// JWKS 验签，并校验 issuer、audience（签发的 client_id）、过期时间与 nonce）。
//
// 这里只用标准库实现 RS256 验签；auth.openai.com 的 JWKS 当前即 RS256。
// 发现文档与 JWKS 均带缓存，避免每次授权都回源。

const (
	siwcJWKSCacheTTL     = 10 * time.Minute
	siwcDiscoveryTimeout = 15 * time.Second
	// 与 ParseIDToken 一致：允许 2 分钟时钟偏差。
	siwcClockSkewTolerance = 120
)

type siwcJWK struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
}

type siwcJWKS struct {
	Keys []siwcJWK `json:"keys"`
}

type siwcOpenIDConfiguration struct {
	Issuer  string `json:"issuer"`
	JWKSURI string `json:"jwks_uri"`
}

type siwcJWKSCacheEntry struct {
	keys      []siwcJWK
	fetchedAt time.Time
}

var (
	siwcJWKSMu    sync.Mutex
	siwcJWKSCache map[string]siwcJWKSCacheEntry
)

func init() {
	siwcJWKSCache = make(map[string]siwcJWKSCacheEntry)
}

// VerifySIWCIDToken 对 SIWC ID token 做 JWKS 验签并校验 iss/aud/exp/nonce。
// expectedAudience 是本次授权使用的 client_id（签发的 oaiapp_... 或注册入口）。
// expectedNonce 为空表示本次流程未使用 nonce（不校验）；非空则必须严格匹配。
func VerifySIWCIDToken(ctx context.Context, idToken, expectedAudience, expectedNonce, proxyURL string) (*SIWCIDTokenClaims, error) {
	if strings.TrimSpace(idToken) == "" {
		return nil, errors.New("siwc id_token is empty")
	}

	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, errors.New("siwc id_token is not a valid JWS compact serialization")
	}
	headerB64, payloadB64, sigB64 := parts[0], parts[1], parts[2]

	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodeJWTSegment(headerB64, &header); err != nil {
		return nil, fmt.Errorf("siwc id_token header: %w", err)
	}
	if header.Alg != "RS256" {
		return nil, fmt.Errorf("siwc id_token unsupported alg %q (want RS256)", header.Alg)
	}

	claims := &SIWCIDTokenClaims{}
	if err := decodeJWTSegment(payloadB64, claims); err != nil {
		return nil, fmt.Errorf("siwc id_token payload: %w", err)
	}
	// aud 兼容字符串与数组两种编码。
	if err := normalizeSIWCAudience(idToken, claims); err != nil {
		return nil, err
	}

	// 1. 签名验证（对 JWKS）。
	signature, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return nil, fmt.Errorf("siwc id_token signature encoding: %w", err)
	}
	signingInput := headerB64 + "." + payloadB64
	digest := sha256.Sum256([]byte(signingInput))
	publicKey, err := siwcResolveRSAKey(ctx, header.Kid, proxyURL)
	if err != nil {
		return nil, err
	}
	if err := rsa.VerifyPKCS1v15(publicKey, crypto.SHA256, digest[:], signature); err != nil {
		return nil, fmt.Errorf("siwc id_token signature verification failed: %w", err)
	}

	// 2. issuer。
	if claims.Iss != SIWCIssuer {
		return nil, fmt.Errorf("siwc id_token issuer mismatch: got %q want %q", claims.Iss, SIWCIssuer)
	}

	// 3. audience：必须包含本次授权使用的 client_id。
	if audience := strings.TrimSpace(expectedAudience); audience != "" {
		found := false
		for _, candidate := range claims.Aud {
			if candidate == audience {
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("siwc id_token audience mismatch: got %v want %q", claims.Aud, audience)
		}
	}

	// 4. 过期时间（带时钟偏差容忍）。
	if claims.Exp > 0 && time.Now().Unix() > claims.Exp+siwcClockSkewTolerance {
		return nil, fmt.Errorf("siwc id_token has expired (exp: %d, now: %d)", claims.Exp, time.Now().Unix())
	}

	// 5. nonce：authorize 请求带了 nonce 时必须严格匹配，防重放/替换。
	if expectedNonce != "" {
		if subtle.ConstantTimeCompare([]byte(claims.Nonce), []byte(expectedNonce)) != 1 {
			return nil, errors.New("siwc id_token nonce mismatch")
		}
	}

	return claims, nil
}

func decodeJWTSegment(segment string, out any) error {
	// OpenAI 的 token 带大量私有声明，这里只做宽松解码（不 DisallowUnknownFields）。
	raw, err := base64.RawURLEncoding.DecodeString(segment)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// normalizeSIWCAudience 把 aud 字段统一成数组：部分实现返回字符串。
func normalizeSIWCAudience(idToken string, claims *SIWCIDTokenClaims) error {
	if len(claims.Aud) > 0 {
		return nil
	}
	payloadB64 := strings.Split(idToken, ".")[1]
	raw, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return err
	}
	var probe struct {
		Aud json.RawMessage `json:"aud"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return err
	}
	trimmed := strings.TrimSpace(string(probe.Aud))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if strings.HasPrefix(trimmed, "\"") {
		var single string
		if err := json.Unmarshal(probe.Aud, &single); err != nil {
			return err
		}
		claims.Aud = []string{single}
	}
	return nil
}

// siwcResolveRSAKey 从（带缓存的）JWKS 中解析指定 kid 的 RSA 公钥。
func siwcResolveRSAKey(ctx context.Context, kid, proxyURL string) (*rsa.PublicKey, error) {
	keys, err := siwcLoadJWKS(ctx, proxyURL)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		if key.Kid != kid {
			continue
		}
		if key.Kty != "RSA" {
			return nil, fmt.Errorf("siwc jwks key %q is %q, want RSA", kid, key.Kty)
		}
		return siwcJWKToRSA(key)
	}
	// kid 未命中可能意味着密钥刚轮换：丢弃缓存强刷一次。
	siwcJWKSMu.Lock()
	delete(siwcJWKSCache, proxyURL)
	siwcJWKSMu.Unlock()
	keys, err = siwcLoadJWKS(ctx, proxyURL)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		if key.Kid == kid {
			return siwcJWKToRSA(key)
		}
	}
	return nil, fmt.Errorf("siwc jwks has no key for kid %q", kid)
}

func siwcJWKToRSA(key siwcJWK) (*rsa.PublicKey, error) {
	modulus, err := base64.RawURLEncoding.DecodeString(key.N)
	if err != nil {
		return nil, fmt.Errorf("siwc jwks modulus: %w", err)
	}
	exponent, err := base64.RawURLEncoding.DecodeString(key.E)
	if err != nil {
		return nil, fmt.Errorf("siwc jwks exponent: %w", err)
	}
	if len(exponent) < 1 || len(exponent) > 8 {
		return nil, errors.New("siwc jwks exponent length invalid")
	}
	var exponentInt int64
	for _, b := range exponent {
		exponentInt = exponentInt<<8 | int64(b)
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(modulus), E: int(exponentInt)}, nil
}

func siwcLoadJWKS(ctx context.Context, proxyURL string) ([]siwcJWK, error) {
	siwcJWKSMu.Lock()
	entry, cached := siwcJWKSCache[proxyURL]
	if cached && time.Since(entry.fetchedAt) < siwcJWKSCacheTTL {
		keys := entry.keys
		siwcJWKSMu.Unlock()
		return keys, nil
	}
	siwcJWKSMu.Unlock()

	transport := &http.Transport{}
	if proxyURL != "" {
		if proxyTransport, err := siwcProxyTransport(proxyURL); err == nil {
			transport = proxyTransport
		}
	}
	httpClient := &http.Client{Transport: transport, Timeout: siwcDiscoveryTimeout}

	// 1. 发现文档 → jwks_uri。
	discoveryReq, err := http.NewRequestWithContext(ctx, http.MethodGet, SIWCOpenIDConfigurationURL, nil)
	if err != nil {
		return nil, err
	}
	discoveryResp, err := httpClient.Do(discoveryReq)
	if err != nil {
		return nil, fmt.Errorf("siwc openid-configuration fetch failed: %w", err)
	}
	defer func() { _ = discoveryResp.Body.Close() }()
	if discoveryResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("siwc openid-configuration fetch failed: status %d", discoveryResp.StatusCode)
	}
	var configuration siwcOpenIDConfiguration
	if err := json.NewDecoder(discoveryResp.Body).Decode(&configuration); err != nil {
		return nil, fmt.Errorf("siwc openid-configuration decode: %w", err)
	}
	if strings.TrimSpace(configuration.JWKSURI) == "" {
		return nil, errors.New("siwc openid-configuration has no jwks_uri")
	}

	// 2. JWKS。
	jwksReq, err := http.NewRequestWithContext(ctx, http.MethodGet, configuration.JWKSURI, nil)
	if err != nil {
		return nil, err
	}
	jwksResp, err := httpClient.Do(jwksReq)
	if err != nil {
		return nil, fmt.Errorf("siwc jwks fetch failed: %w", err)
	}
	defer func() { _ = jwksResp.Body.Close() }()
	if jwksResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("siwc jwks fetch failed: status %d", jwksResp.StatusCode)
	}
	var keySet siwcJWKS
	if err := json.NewDecoder(jwksResp.Body).Decode(&keySet); err != nil {
		return nil, fmt.Errorf("siwc jwks decode: %w", err)
	}
	if len(keySet.Keys) == 0 {
		return nil, errors.New("siwc jwks is empty")
	}

	siwcJWKSMu.Lock()
	siwcJWKSCache[proxyURL] = siwcJWKSCacheEntry{keys: keySet.Keys, fetchedAt: time.Now()}
	siwcJWKSMu.Unlock()
	return keySet.Keys, nil
}

func siwcProxyTransport(proxyURL string) (*http.Transport, error) {
	parsed, err := url.Parse(strings.TrimSpace(proxyURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid proxy url for siwc jwks fetch: %q", proxyURL)
	}
	return &http.Transport{Proxy: http.ProxyURL(parsed)}, nil
}
