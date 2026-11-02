package trap

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"siren/internal/config"
)

// dialTLS performs a TLS handshake against addr without certificate
// verification (the client does not pin the trap's self-signed key).
func dialTLS(t *testing.T, addr string) *tls.Conn {
	t.Helper()
	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 3 * time.Second},
		"tcp", addr,
		&tls.Config{InsecureSkipVerify: true}, //nolint:gosec // decoy service, no pinned key
	)
	if err != nil {
		t.Fatalf("tls dial %s: %v", addr, err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func TestTLSHandshakeAndHTTPDialog(t *testing.T) {
	dir := t.TempDir()
	factory := NewTrapFactory(dir)

	port := freePort(t)
	emu, err := factory(config.Trap{Port: port, Enabled: true, Emulator: "http", TLS: true})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _ = emu.Handle(context.Background(), conn) }()
		}
	}()

	conn := dialTLS(t, "127.0.0.1:"+strconv.Itoa(port))
	if _, err := conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	resp := readUntil(t, conn, "\r\n\r\n")
	if !strings.Contains(resp, "HTTP/1.1 200 OK") || !strings.Contains(resp, "Content-Type: text/html") {
		t.Errorf("TLS dialog response headers wrong: %q", resp)
	}
}

func TestTLSCertPersisted(t *testing.T) {
	dir := t.TempDir()
	factory := NewTrapFactory(dir)

	trapCfg := config.Trap{Port: 443, Enabled: true, Emulator: "http", TLS: true}
	if _, err := factory(trapCfg); err != nil {
		t.Fatalf("first factory call: %v", err)
	}
	certPath := filepath.Join(dir, tlsCertFile)
	keyPath := filepath.Join(dir, tlsKeyFile)
	for _, p := range []string{certPath, keyPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("cert file %s missing after first run: %v", p, err)
		}
	}
	certBefore, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}
	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	// Second run must reuse, not regenerate.
	if _, err := factory(trapCfg); err != nil {
		t.Fatalf("second factory call: %v", err)
	}
	certAfter, _ := os.ReadFile(certPath)
	keyAfter, _ := os.ReadFile(keyPath)
	if string(certBefore) != string(certAfter) || string(keyBefore) != string(keyAfter) {
		t.Error("certificate must be reused across runs, not regenerated")
	}
	// The persisted pair must parse as a valid TLS keypair.
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		t.Errorf("persisted keypair does not parse: %v", err)
	}
}

func TestTLSPlainTextClientIsDropped(t *testing.T) {
	// A plain-HTTP client hitting a TLS port must not crash the emulator:
	// this is the most common scanner mistake.
	dir := t.TempDir()
	factory := NewTrapFactory(dir)
	port := freePort(t)
	emu, err := factory(config.Trap{Port: port, Enabled: true, Emulator: "http", TLS: true})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		done <- emu.Handle(context.Background(), conn)
	}()

	plain, err := net.Dial("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	_, _ = plain.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
	_ = plain.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("emulator did not return after plain client disconnect")
	}
}
