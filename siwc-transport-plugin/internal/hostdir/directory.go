// Package hostdir wraps the plugin's reverse connection to the host
// HostService: the account directory (SIWC channel detection) and outbound
// identity resolution (token / proxy / headers).
package hostdir

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	pluginv1 "github.com/Wei-Shaw/sub2api/pkg/pluginapi/v1"
	"google.golang.org/grpc"
)

// HostServiceAPIVersionMin is the host service contract version this plugin
// needs: from v2 on, ListAccounts carries the full readable metadata used to
// detect the SIWC channel.
const HostServiceAPIVersionMin = 2

// Directory serves account directory lookups from a TTL cache and resolves
// outbound identities.
type Directory struct {
	mu sync.Mutex
	// client is injected by InitHostServices; nil means the host offers no
	// reverse service.
	client pluginv1.HostServiceClient
	// supportedHostAPIVersion is the host service version from the handshake.
	supportedHostAPIVersion uint32

	// cached is the latest account directory snapshot.
	cached         map[int64]AccountMeta
	cachedAt       time.Time
	ttl            time.Duration
	lastRefreshErr error

	// refreshing is the ListAccounts call in flight, shared by every caller
	// that needs the directory meanwhile; nil when none runs.
	refreshing *directoryRefresh
	// generation changes on every host re-handshake, so a refresh started on
	// the previous connection never replaces the new cache.
	generation uint64
}

type directoryRefresh struct {
	done     chan struct{}
	accounts map[int64]AccountMeta
	err      error
}

// AccountMeta is the view of a host account (from its metadata_json) that
// forwarding decisions need.
type AccountMeta struct {
	ID          int64
	Name        string
	Status      string
	Schedulable bool
	// SIWC reports that the account uses the SIWC token-sharing channel (the
	// host passes extra.auth_provider from account creation into metadata).
	SIWC bool
}

// New creates a directory client. ttl is the cache lifetime (30s when <= 0).
func New(ttl time.Duration) *Directory {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &Directory{ttl: ttl, cached: make(map[int64]AccountMeta)}
}

// AttachClient injects the host service connection after a successful
// InitHostServices handshake.
func (d *Directory) AttachClient(client pluginv1.HostServiceClient, hostAPIVersion uint32) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.client = client
	d.supportedHostAPIVersion = hostAPIVersion
	// A new handshake invalidates the cache and any refresh of the old one.
	d.cached = make(map[int64]AccountMeta)
	d.cachedAt = time.Time{}
	d.refreshing = nil
	d.generation++
}

// AttachConn is the AttachClient shorthand that builds the client from a gRPC
// connection.
func (d *Directory) AttachConn(conn *grpc.ClientConn, hostAPIVersion uint32) {
	d.AttachClient(pluginv1.NewHostServiceClient(conn), hostAPIVersion)
}

// Available reports whether the host service is attached with a sufficient
// version.
func (d *Directory) Available() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.client != nil && d.supportedHostAPIVersion >= HostServiceAPIVersionMin
}

// HostAPIVersion returns the handshaken host service version (0 before the
// handshake).
func (d *Directory) HostAPIVersion() uint32 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.supportedHostAPIVersion
}

// Account reports the SIWC classification of one account. A cached entry is
// returned at once and refreshed in the background once it is older than the
// TTL (an account's channel never changes). A miss, typically a freshly
// imported account, waits for a refresh and reads that refresh's own result:
// forwarding a SIWC account down the non-SIWC path fails upstream with a 401.
func (d *Directory) Account(ctx context.Context, accountID int64) (AccountMeta, bool) {
	d.mu.Lock()
	meta, ok := d.cached[accountID]
	stale := time.Since(d.cachedAt) > d.ttl
	d.mu.Unlock()
	if ok {
		if stale {
			d.startRefresh(ctx)
		}
		return meta, true
	}

	refresh := d.startRefresh(ctx)
	if refresh == nil {
		return AccountMeta{}, false
	}
	select {
	case <-refresh.done:
	case <-ctx.Done():
		return AccountMeta{}, false
	}
	if refresh.err != nil {
		return AccountMeta{}, false
	}
	meta, ok = refresh.accounts[accountID]
	return meta, ok
}

// startRefresh joins the refresh in flight or starts one, and returns nil when
// no host connection is attached. The refresh is detached from ctx's
// cancellation because other requests share its result.
func (d *Directory) startRefresh(ctx context.Context) *directoryRefresh {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.refreshing != nil {
		return d.refreshing
	}
	if d.client == nil {
		return nil
	}
	refresh := &directoryRefresh{done: make(chan struct{})}
	d.refreshing = refresh
	go d.runRefresh(context.WithoutCancel(ctx), d.client, d.generation, refresh)
	return refresh
}

func (d *Directory) runRefresh(ctx context.Context, client pluginv1.HostServiceClient, generation uint64, refresh *directoryRefresh) {
	refresh.accounts, refresh.err = listAccounts(ctx, client)
	d.mu.Lock()
	if d.refreshing == refresh {
		d.refreshing = nil
	}
	if d.generation == generation {
		if refresh.err == nil {
			d.cached = refresh.accounts
			d.lastRefreshErr = nil
		} else {
			d.lastRefreshErr = refresh.err
		}
		// A failed refresh keeps the previous snapshot; cached entries are
		// retried after another TTL, misses retry right away.
		d.cachedAt = time.Now()
	}
	d.mu.Unlock()
	close(refresh.done)
}

// LastRefreshError returns the error of the latest directory refresh
// (diagnostics).
func (d *Directory) LastRefreshError() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastRefreshErr
}

// CachedCount returns the number of cached accounts (status page).
func (d *Directory) CachedCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.cached)
}

func listAccounts(ctx context.Context, client pluginv1.HostServiceClient) (map[int64]AccountMeta, error) {
	resp, err := client.ListAccounts(ctx, &pluginv1.ListAccountsRequest{
		Platform:    "openai",
		AccountType: "oauth",
	})
	if err != nil {
		return nil, fmt.Errorf("host ListAccounts: %w", err)
	}
	accounts := make(map[int64]AccountMeta, len(resp.GetAccounts()))
	for _, account := range resp.GetAccounts() {
		meta := AccountMeta{
			ID:          account.GetId(),
			Name:        account.GetName(),
			Status:      account.GetStatus(),
			Schedulable: account.GetSchedulable(),
		}
		meta.SIWC = snapshotIsSIWC(account.GetMetadataJson())
		accounts[account.GetId()] = meta
	}
	return accounts, nil
}

// snapshotIsSIWC detects the SIWC channel from the host metadata_json. The host
// writes extra.auth_provider == "siwc" when it creates a SIWC account;
// credentials (including auth_mode) never reach the plugin's snapshot, so extra
// is authoritative.
func snapshotIsSIWC(metadataJSON []byte) bool {
	if len(metadataJSON) == 0 {
		return false
	}
	var snapshot struct {
		Extra *struct {
			AuthProvider string `json:"auth_provider"`
		} `json:"extra"`
	}
	if err := json.Unmarshal(metadataJSON, &snapshot); err != nil {
		return false
	}
	return snapshot.Extra != nil && snapshot.Extra.AuthProvider == "siwc"
}

// OutboundIdentity is the identity material of one outbound request.
type OutboundIdentity struct {
	AccountID int64
	ProxyURL  string
	Token     string
	Headers   map[string]*pluginv1.HeaderValues
}

// ResolveOutboundIdentity asks the host for the account's outbound identity
// (including a token refreshed when needed).
func (d *Directory) ResolveOutboundIdentity(ctx context.Context, accountID int64) (*OutboundIdentity, error) {
	d.mu.Lock()
	client := d.client
	d.mu.Unlock()
	if client == nil {
		return nil, fmt.Errorf("host service unavailable")
	}
	resp, err := client.ResolveOutboundIdentity(ctx, &pluginv1.ResolveOutboundIdentityRequest{AccountId: accountID})
	if err != nil {
		return nil, fmt.Errorf("host ResolveOutboundIdentity: %w", err)
	}
	if !resp.GetFound() {
		return nil, fmt.Errorf("outbound identity not found for account %d", accountID)
	}
	if resp.GetToken() == "" {
		return nil, fmt.Errorf("outbound identity for account %d carries no token", accountID)
	}
	return &OutboundIdentity{
		AccountID: resp.GetAccountId(),
		ProxyURL:  resp.GetProxyUrl(),
		Token:     resp.GetToken(),
		Headers:   resp.GetHeaders(),
	}, nil
}
