package core

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"testing"
	"time"
)

func selfSignedCert(t *testing.T, notBefore, notAfter time.Time) *x509.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ecdsa.GenerateKey: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "rdp-test-host"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("x509.CreateCertificate: %v", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("x509.ParseCertificate: %v", err)
	}
	return cert
}

func TestVerifyRDPServerCertificate(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name    string
		certs   []*x509.Certificate
		wantErr bool
	}{
		{
			name:    "no certificate presented",
			certs:   nil,
			wantErr: true,
		},
		{
			name:    "currently valid self-signed certificate",
			certs:   []*x509.Certificate{selfSignedCert(t, now.Add(-time.Hour), now.Add(time.Hour))},
			wantErr: false,
		},
		{
			name:    "expired certificate",
			certs:   []*x509.Certificate{selfSignedCert(t, now.Add(-48*time.Hour), now.Add(-24*time.Hour))},
			wantErr: true,
		},
		{
			name:    "not yet valid certificate",
			certs:   []*x509.Certificate{selfSignedCert(t, now.Add(24*time.Hour), now.Add(48*time.Hour))},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := verifyRDPServerCertificate(tls.ConnectionState{PeerCertificates: tc.certs})
			if (err != nil) != tc.wantErr {
				t.Fatalf("verifyRDPServerCertificate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
