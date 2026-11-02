package trap

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"siren/internal/config"
)

// Certificate file names inside the agent data directory.
const (
	tlsCertFile = "trap-cert.pem"
	tlsKeyFile  = "trap-key.pem"
)

// LoadOrCreateTLSCert returns the trap TLS certificate, generating a
// self-signed ECDSA one on first use and persisting it in dataDir for all
// subsequent runs.
func LoadOrCreateTLSCert(dataDir string) (tls.Certificate, error) {
	certPath := filepath.Join(dataDir, tlsCertFile)
	keyPath := filepath.Join(dataDir, tlsKeyFile)

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err == nil {
		return cert, nil
	}
	if !os.IsNotExist(err) {
		return tls.Certificate{}, fmt.Errorf("trap: load TLS keypair: %w", err)
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("trap: generate TLS key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("trap: generate serial: %w", err)
	}
	tpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "siren-trap"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(1, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tpl, &tpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("trap: create certificate: %w", err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return tls.Certificate{}, fmt.Errorf("trap: create data dir %s: %w", dataDir, err)
	}
	keyPEM, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("trap: marshal key: %w", err)
	}
	files := []struct {
		path string
		body []byte
		mode os.FileMode
	}{
		{certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644},
		{keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyPEM}), 0o600},
	}
	for _, f := range files {
		if err := os.WriteFile(f.path, f.body, f.mode); err != nil {
			return tls.Certificate{}, fmt.Errorf("trap: write %s: %w", f.path, err)
		}
	}
	cert, err = tls.X509KeyPair(files[0].body, files[1].body)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("trap: parse fresh keypair: %w", err)
	}
	return cert, nil
}

// tlsEmulator wraps an inner emulator into a TLS server. After the handshake
// the inner emulator sees a plain net.Conn, exactly as with real TLS
// terminators.
type tlsEmulator struct {
	inner Emulator
	cfg   *tls.Config
}

// Handle implements Emulator.
func (e *tlsEmulator) Handle(ctx context.Context, conn net.Conn) error {
	tc := tls.Server(conn, e.cfg)
	if err := tc.SetDeadline(time.Now().Add(dialogTimeout)); err != nil {
		return err
	}
	if err := tc.HandshakeContext(ctx); err != nil {
		// Failed handshakes (port scans, plain HTTP to a TLS port) are
		// normal background noise.
		return errClientClosed
	}
	// The dialog may take longer than the handshake.
	if err := tc.SetDeadline(time.Time{}); err != nil {
		return err
	}
	return e.inner.Handle(ctx, tc)
}

// NewTrapFactory returns the emulator factory bound to dataDir, where the
// TLS certificate lives. Traps with TLS enabled share one certificate.
func NewTrapFactory(dataDir string) Factory {
	return func(t config.Trap) (Emulator, error) {
		inner, err := defaultFactory(t)
		if err != nil {
			return nil, err
		}
		if !t.TLS {
			return inner, nil
		}
		cert, err := LoadOrCreateTLSCert(dataDir)
		if err != nil {
			return nil, err
		}
		return &tlsEmulator{
			inner: inner,
			cfg:   &tls.Config{Certificates: []tls.Certificate{cert}},
		}, nil
	}
}
