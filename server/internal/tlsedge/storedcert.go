package tlsedge

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
)

// certificatesPrefix is the storage prefix certmagic keeps issued material
// under. The issuer key sits between it and the hostname and depends on the
// configured CA, so lookups match on the "<host>/<host>.<ext>" suffix rather
// than reconstructing the whole key.
const certificatesPrefix = "certificates"

// ErrNoStoredCertificate is returned when tls_storage holds no usable
// certificate for a hostname.
var ErrNoStoredCertificate = errors.New("tlsedge: no stored certificate for host")

// LoadStoredCertificate returns the certificate and private key held in
// tls_storage for host, and the parsed leaf.
//
// It reads storage directly rather than going through certmagic, on purpose:
// the caller is on the handshake path for a host certmagic has been told we do
// NOT own, so asking certmagic for a certificate would (correctly) take the
// issuance path and be refused. What we want here is strictly "hand back the
// material we already have", with no issuance, no renewal and no CA traffic.
func LoadStoredCertificate(ctx context.Context, dbSvc db.Service, host string) (*tls.Certificate, error) {
	host = normalizeHost(host)
	if host == "" || dbSvc == nil {
		return nil, ErrNoStoredCertificate
	}

	infos, err := dbSvc.TLSStorageList(ctx, certificatesPrefix)
	if err != nil {
		return nil, fmt.Errorf("tlsedge: list stored certificates: %w", err)
	}

	suffix := "/" + host + "/" + host + ".crt"

	certKey := ""

	for i := range infos {
		if strings.HasSuffix(infos[i].Key, suffix) {
			certKey = infos[i].Key

			break
		}
	}

	if certKey == "" {
		return nil, ErrNoStoredCertificate
	}

	certPEM, err := dbSvc.TLSStorageLoad(ctx, certKey)
	if err != nil {
		return nil, fmt.Errorf("tlsedge: load stored certificate: %w", err)
	}

	keyPEM, err := dbSvc.TLSStorageLoad(ctx, strings.TrimSuffix(certKey, ".crt")+".key")
	if err != nil {
		return nil, fmt.Errorf("tlsedge: load stored private key: %w", err)
	}

	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("tlsedge: stored certificate for %s is unusable: %w", host, err)
	}

	if pair.Leaf == nil {
		leaf, parseErr := x509.ParseCertificate(pair.Certificate[0])
		if parseErr != nil {
			return nil, fmt.Errorf("tlsedge: stored certificate for %s does not parse: %w", host, parseErr)
		}

		pair.Leaf = leaf
	}

	return &pair, nil
}

// StoredCertificateUsable reports whether a stored certificate is currently
// within its validity window. An expired certificate is worse than none: it
// still fails at the handshake, with a browser interstitial instead of a
// legible page, which is the whole failure mode this exists to avoid.
func StoredCertificateUsable(cert *tls.Certificate, now time.Time) bool {
	if cert == nil || cert.Leaf == nil {
		return false
	}

	return now.After(cert.Leaf.NotBefore) && now.Before(cert.Leaf.NotAfter)
}

// HasValidStoredCertificate reports whether tls_storage holds a present,
// unexpired certificate for host.
//
// Exported for the custom-domain re-verification sweep, which uses it as the
// second half of the re-promotion gate: a demoted domain that has started
// resolving to us again is only trusted back if we are still holding the
// certificate we obtained for it. It takes a db.Service rather than an *Edge
// so the job can call it without depending on in-server TLS being wired up.
func HasValidStoredCertificate(ctx context.Context, dbSvc db.Service, host string, now time.Time) bool {
	cert, err := LoadStoredCertificate(ctx, dbSvc, host)
	if err != nil {
		return false
	}

	return StoredCertificateUsable(cert, now)
}
