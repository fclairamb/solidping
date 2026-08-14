// Package tlsedge implements in-server TLS for SolidPing: on-demand Let's
// Encrypt certificate issuance (via certmagic) for the instance's own hosts and
// for verified status-page custom domains, with every TLS asset persisted in
// the database so a cluster shares one set of certificates and a restart never
// re-issues.
//
// It is entirely opt-in (config acme.enabled). With it off, nothing in this
// package runs and TLS stays where it was: an external proxy gated on
// GET /api/v1/public/custom-domains/allowed.
//
// SECURITY: the storage tables hold ACME account keys and certificate PRIVATE
// KEYS. Nothing here is reachable from an HTTP handler, and the keys must never
// be surfaced through an API, export, or debug endpoint.
package tlsedge

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/caddyserver/certmagic"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Lock tuning. certmagic only needs coarse, single-writer-per-key locking
// around issuance/renewal, so an expiring row plus a background refresher is
// enough (the package explicitly tolerates mis-synchronization).
const (
	// lockTTL is how long an acquired lock stays valid without a refresh. It
	// must comfortably exceed lockRefreshEvery so a healthy holder never loses
	// its lock to a scheduling hiccup, while still letting a crashed process's
	// lock be reclaimed in bounded time.
	lockTTL = 2 * time.Minute
	// lockRefreshEvery is the refresher's period.
	lockRefreshEvery = 20 * time.Second
	// lockPollEvery is how often a blocked Lock retries.
	lockPollEvery = time.Second
)

// Storage is a certmagic.Storage backed by the SolidPing database
// (tls_storage / tls_storage_locks). It is safe for concurrent use and honors
// context cancellation, as certmagic requires.
type Storage struct {
	db db.Service
	// nodeID identifies this process in lock rows; combined with a per-lock
	// random suffix it makes ownership unforgeable and keeps two Lock calls in
	// the same process mutually exclusive.
	nodeID string

	mu sync.Mutex
	// held maps a lock name to its owner token and the cancel func of its
	// refresher goroutine.
	held map[string]heldLock
}

type heldLock struct {
	owner  string
	cancel context.CancelFunc
}

// NewStorage returns a database-backed certmagic.Storage.
func NewStorage(dbService db.Service) *Storage {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "node"
	}

	return &Storage{
		db:     dbService,
		nodeID: host + "-" + strconv.Itoa(os.Getpid()),
		held:   make(map[string]heldLock),
	}
}

// Store puts value at key.
func (s *Storage) Store(ctx context.Context, key string, value []byte) error {
	return s.db.TLSStorageStore(ctx, normalizeKey(key), value)
}

// Load retrieves the value at key, returning fs.ErrNotExist when it is absent
// (the error certmagic tests for).
func (s *Storage) Load(ctx context.Context, key string) ([]byte, error) {
	value, err := s.db.TLSStorageLoad(ctx, normalizeKey(key))
	if err != nil {
		return nil, mapNotExist(key, err)
	}

	return value, nil
}

// Delete removes key and everything nested under it.
func (s *Storage) Delete(ctx context.Context, key string) error {
	return s.db.TLSStorageDelete(ctx, normalizeKey(key))
}

// DeleteSite removes every stored asset for one domain — certificate, private
// key and metadata — by dropping the folder certmagic keeps them in. Delete
// already removes everything nested under a key, so the prefix is enough and
// the three files do not need naming individually.
//
// Used when a domain stops being ours (unmapped, or demoted by the
// re-verification sweep): leaving a live private key in the database for a
// hostname someone else may now control is not worth the convenience of a
// warm cache if it ever comes back.
func (s *Storage) DeleteSite(ctx context.Context, issuerKey, domain string) error {
	return s.Delete(ctx, certmagic.StorageKeys.CertsSitePrefix(issuerKey, domain))
}

// Exists reports whether key exists as a value or as a prefix of other keys
// (certmagic treats both as "exists").
func (s *Storage) Exists(ctx context.Context, key string) bool {
	normalized := normalizeKey(key)

	exists, err := s.db.TLSStorageExists(ctx, normalized)
	if err != nil {
		return false
	}

	if exists {
		return true
	}

	children, err := s.db.TLSStorageList(ctx, normalized)

	return err == nil && len(children) > 0
}

// List returns the keys under prefix. With recursive=false only the immediate
// children are returned, with nested keys collapsed to their directory key —
// the file-system semantics certmagic's docs describe.
func (s *Storage) List(ctx context.Context, prefix string, recursive bool) ([]string, error) {
	normalized := normalizeKey(prefix)

	infos, err := s.db.TLSStorageList(ctx, normalized)
	if err != nil {
		return nil, err
	}

	if len(infos) == 0 {
		return nil, mapNotExist(prefix, sql.ErrNoRows)
	}

	keys := make([]string, 0, len(infos))
	for i := range infos {
		keys = append(keys, infos[i].Key)
	}

	if recursive {
		return keys, nil
	}

	return immediateChildren(normalized, keys), nil
}

// immediateChildren collapses a recursive key list to the direct children of
// prefix, de-duplicated and sorted. A key nested deeper contributes its
// first path component under prefix (a "directory").
func immediateChildren(prefix string, keys []string) []string {
	seen := make(map[string]struct{}, len(keys))
	out := make([]string, 0, len(keys))

	for _, key := range keys {
		if key == prefix {
			continue
		}

		rest := key
		if prefix != "" {
			rest = strings.TrimPrefix(key, prefix+"/")
			if rest == key {
				continue
			}
		}

		if idx := strings.Index(rest, "/"); idx >= 0 {
			rest = rest[:idx]
		}

		child := rest
		if prefix != "" {
			child = prefix + "/" + rest
		}

		if _, dup := seen[child]; dup {
			continue
		}

		seen[child] = struct{}{}

		out = append(out, child)
	}

	sort.Strings(out)

	return out
}

// Stat returns information about key. Directories (keys that only act as a
// prefix) report IsTerminal=false, as certmagic expects.
func (s *Storage) Stat(ctx context.Context, key string) (certmagic.KeyInfo, error) {
	normalized := normalizeKey(key)

	info, err := s.db.TLSStorageStat(ctx, normalized)
	if err == nil {
		return certmagic.KeyInfo{
			Key:        info.Key,
			Modified:   info.ModifiedAt,
			Size:       info.Size,
			IsTerminal: true,
		}, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return certmagic.KeyInfo{}, err
	}

	// Not a file — it may still be a directory (a prefix of other keys), whose
	// modification time is the newest of its children.
	children, listErr := s.db.TLSStorageList(ctx, normalized)
	if listErr != nil {
		return certmagic.KeyInfo{}, listErr
	}

	if len(children) == 0 {
		return certmagic.KeyInfo{}, mapNotExist(key, err)
	}

	return certmagic.KeyInfo{
		Key:        normalized,
		Modified:   newestModified(children),
		IsTerminal: false,
	}, nil
}

func newestModified(infos []models.TLSStorageKeyInfo) time.Time {
	var newest time.Time
	for i := range infos {
		if infos[i].ModifiedAt.After(newest) {
			newest = infos[i].ModifiedAt
		}
	}

	return newest
}

// Lock blocks until the named lock is acquired, the lock's holder goes stale,
// or ctx is done. A refresher goroutine keeps the lease alive for as long as
// this process holds it.
func (s *Storage) Lock(ctx context.Context, name string) error {
	owner, err := s.newOwnerToken()
	if err != nil {
		return err
	}

	ticker := time.NewTicker(lockPollEvery)
	defer ticker.Stop()

	for {
		acquired, acquireErr := s.db.TLSStorageAcquireLock(ctx, name, owner, time.Now().Add(lockTTL))
		if acquireErr != nil {
			return acquireErr
		}

		if acquired {
			// contextcheck: the refresher must OUTLIVE the acquiring context —
			// it is tied to Unlock, not to the caller's request.
			s.startRefresher(name, owner) //nolint:contextcheck

			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Unlock releases a lock this process holds and stops its refresher.
func (s *Storage) Unlock(ctx context.Context, name string) error {
	s.mu.Lock()
	lock, ok := s.held[name]
	delete(s.held, name)
	s.mu.Unlock()

	if !ok {
		// Never held here (or already released) — nothing to do, and we must
		// not delete a row another node owns.
		return nil
	}

	lock.cancel()

	return s.db.TLSStorageReleaseLock(ctx, name, lock.owner)
}

// startRefresher keeps a held lock's lease alive until Unlock (or until the
// lock is lost, e.g. because a peer reclaimed a lease this process failed to
// refresh in time).
func (s *Storage) startRefresher(name, owner string) {
	// The refresher must outlive the request/caller context that acquired the
	// lock — it is tied to Unlock instead.
	ctx, cancel := context.WithCancel(context.Background())

	s.mu.Lock()
	s.held[name] = heldLock{owner: owner, cancel: cancel}
	s.mu.Unlock()

	go func() {
		ticker := time.NewTicker(lockRefreshEvery)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				stillMine, err := s.db.TLSStorageRefreshLock(ctx, name, owner, time.Now().Add(lockTTL))
				if err != nil || !stillMine {
					return
				}
			}
		}
	}()
}

// newOwnerToken returns a per-acquisition identity: the node id plus fresh
// randomness, so two Lock calls in the same process never share ownership.
func (s *Storage) newOwnerToken() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("tlsedge: generate lock owner: %w", err)
	}

	return s.nodeID + "/" + hex.EncodeToString(buf), nil
}

// normalizeKey strips leading/trailing slashes and collapses the path, so the
// same logical asset always lands on one row regardless of how certmagic
// spelled it.
func normalizeKey(key string) string {
	cleaned := path.Clean("/" + strings.TrimSpace(key))

	return strings.Trim(cleaned, "/")
}

// mapNotExist translates a "no rows" database error into fs.ErrNotExist, which
// is what certmagic's Load/Delete/List/Stat contract requires.
func mapNotExist(key string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("tlsedge: key %q: %w", key, fs.ErrNotExist)
	}

	return err
}

// Storage must satisfy certmagic's interface.
var _ certmagic.Storage = (*Storage)(nil)
