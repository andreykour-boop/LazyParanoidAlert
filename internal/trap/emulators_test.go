package trap

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"siren/internal/config"
	"siren/internal/event"
	"siren/internal/logsetup"
)

// pipeEmulator runs emu on one end of an in-memory pipe and drives the other
// end with client. It fails the test on emulator errors other than clean
// client disconnects.
func pipeEmulator(t *testing.T, emu Emulator) net.Conn {
	t.Helper()
	server, client := net.Pipe()
	done := make(chan error, 1)
	go func() { done <- emu.Handle(context.Background(), server) }()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("emulator did not return after pipe close")
		}
	})
	return client
}

// readUntil accumulates everything received until want appears, failing on
// deadline. Works across multiple lines, unlike a single ReadString.
func readUntil(t *testing.T, conn net.Conn, want string) string {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	r := bufio.NewReader(conn)
	var acc strings.Builder
	for {
		line, err := r.ReadString('\n')
		acc.WriteString(line)
		if strings.Contains(acc.String(), want) {
			return acc.String()
		}
		if err != nil {
			t.Fatalf("read: %v (got %q, want substring %q)", err, acc.String(), want)
		}
	}
}

func readN(t *testing.T, conn net.Conn, n int) []byte {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read %d bytes: %v", n, err)
	}
	return buf
}

func writeString(t *testing.T, conn net.Conn, s string) {
	t.Helper()
	if err := conn.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := conn.Write([]byte(s)); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestEmulatorFTPDialog(t *testing.T) {
	conn := pipeEmulator(t, EmulatorFTPT{})

	banner := readUntil(t, conn, "220")
	if !strings.Contains(banner, "FTP") {
		t.Errorf("banner %q does not look like FTP", banner)
	}
	writeString(t, conn, "USER admin\r\n")
	readUntil(t, conn, "331")
	writeString(t, conn, "PASS secret\r\n")
	readUntil(t, conn, "530")
	writeString(t, conn, "QUIT\r\n")
	readUntil(t, conn, "221")
}

func TestEmulatorTelnetDialog(t *testing.T) {
	conn := pipeEmulator(t, EmulatorTelnetT{})

	readUntil(t, conn, "login:")
	writeString(t, conn, "admin\r\n")
	readUntil(t, conn, "Password:")
	writeString(t, conn, "hunter2\r\n")
	readUntil(t, conn, "Login incorrect")
}

func TestEmulatorHTTPDialog(t *testing.T) {
	conn := pipeEmulator(t, EmulatorHTTPT{})

	writeString(t, conn, "GET / HTTP/1.1\r\nHost: x\r\n\r\n")
	headers := readUntil(t, conn, "\r\n\r\n")
	if !strings.Contains(headers, "HTTP/1.1 200 OK") {
		t.Errorf("status line missing: %q", headers)
	}
	if !strings.Contains(headers, "Content-Length:") {
		t.Errorf("headers missing Content-Length: %q", headers)
	}
}

func TestEmulatorVNCDialog(t *testing.T) {
	conn := pipeEmulator(t, EmulatorVNCT{})

	if got := string(readN(t, conn, 12)); got != "RFB 003.008\n" {
		t.Fatalf("server version = %q", got)
	}
	writeString(t, conn, "RFB 003.008\n")
	sec := readN(t, conn, 2)
	if sec[0] != 1 || sec[1] != 1 {
		t.Fatalf("security types = %v, want [1 1] (None)", sec)
	}
	writeString(t, conn, "\x01")
	if got := readN(t, conn, 4); binary.BigEndian.Uint32(got) != 0 {
		t.Fatalf("security result = %v, want OK", got)
	}
	writeString(t, conn, "\x00") // ClientInit: do not share
	si := readN(t, conn, 24)
	if w := binary.BigEndian.Uint16(si[0:2]); w == 0 {
		t.Error("ServerInit width must be non-zero")
	}
}

func TestEmulatorMySQLGreeting(t *testing.T) {
	conn := pipeEmulator(t, EmulatorMySQLT{})

	// MySQL packet: 3-byte length, 1-byte sequence, payload.
	hdr := readN(t, conn, 4)
	plen := int(hdr[0]) | int(hdr[1])<<8 | int(hdr[2])<<16
	payload := readN(t, conn, plen)
	if payload[0] != 10 {
		t.Fatalf("protocol version = %d, want 10", payload[0])
	}
	// Server version ends at the first NUL after the protocol byte.
	end := 1
	for payload[end] != 0 {
		end++
	}
	version := string(payload[1:end])
	if !strings.HasPrefix(version, "8.") && !strings.HasPrefix(version, "5.") {
		t.Errorf("server version %q does not look like MySQL", version)
	}

	// Client sends a (malformed but non-empty) handshake response; the
	// emulator must answer with an access-denied ERR packet.
	writeString(t, conn, "\x00\x00")
	errHdr := readN(t, conn, 4)
	elen := int(errHdr[0]) | int(errHdr[1])<<8 | int(errHdr[2])<<16
	errPayload := readN(t, conn, elen)
	if errPayload[0] != 0xff {
		t.Fatalf("expected ERR packet (0xff), got %d", errPayload[0])
	}
	code := binary.BigEndian.Uint16(errPayload[1:3])
	if code != 1045 {
		t.Errorf("error code = %d, want 1045 (access denied)", code)
	}
}

func TestEmulatorPostgresDialog(t *testing.T) {
	conn := pipeEmulator(t, EmulatorPostgresT{})

	req := readN(t, conn, 9)
	if req[0] != 'R' {
		t.Fatalf("first message type = %q, want 'R'", req[0])
	}
	if binary.BigEndian.Uint32(req[5:9]) != 3 {
		t.Fatalf("auth request kind = %d, want 3 (cleartext password)", binary.BigEndian.Uint32(req[5:9]))
	}
	// Client answers with a PasswordMessage: 'p', total length 10
	// (4 header + 6 body), body "secret".
	writeString(t, conn, "p\x00\x00\x00\x0asecret")
	errMsg := readN(t, conn, 5)
	if errMsg[0] != 'E' {
		t.Fatalf("expected ErrorResponse ('E'), got %q", errMsg[0])
	}
	// Drain the rest of the ErrorResponse per its own length field.
	el := int(binary.BigEndian.Uint32(errMsg[1:5]))
	if el > 5 {
		readN(t, conn, el-5)
	}
}

func TestEmulatorRDPNegotiate(t *testing.T) {
	conn := pipeEmulator(t, EmulatorRDPT{})

	// X.224 Connection Request (TPKT + CR with negotiation request).
	writeString(t, conn, "\x03\x00\x00\x13\x0e\xe0\x00\x00\x00\x00\x00\x01\x00\x08\x00\x03\x00\x00\x00")
	cc := readN(t, conn, 19)
	if cc[0] != 3 || cc[4] != 0x0e || cc[5] != 0xd0 {
		t.Fatalf("connection confirm header = % x", cc)
	}
	// Negotiation response selects PROTOCOL_HYBRID.
	neg := cc[11:19]
	if neg[0] != 0x02 {
		t.Fatalf("negotiation type = %d, want 2 (NEG_RSP)", neg[0])
	}
	if binary.LittleEndian.Uint32(neg[4:8]) != 3 {
		t.Errorf("selected protocol = %d, want 3 (HYBRID)", binary.LittleEndian.Uint32(neg[4:8]))
	}
}

func TestEmulatorSilentStaysQuiet(t *testing.T) {
	conn := pipeEmulator(t, EmulatorSilentT{})
	if err := conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	buf := make([]byte, 1)
	if n, err := conn.Read(buf); n != 0 {
		t.Fatalf("silent emulator must not send bytes, read %d (%v)", n, err)
	}
}

func TestSSHHandshakeAndAuthReject(t *testing.T) {
	emu, err := newSSHEmulator(2222)
	if err != nil {
		t.Fatalf("newSSHEmulator: %v", err)
	}
	// Real TCP, not net.Pipe: both SSH sides write their version strings
	// before reading, which deadlocks on an unbuffered pipe.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	emuDone := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			emuDone <- err
			return
		}
		emuDone <- emu.Handle(context.Background(), conn)
	}()

	cfg := &ssh.ClientConfig{
		User: "admin",
		Auth: []ssh.AuthMethod{ssh.Password("wrong-password")},
		HostKeyCallback: func(string, net.Addr, ssh.PublicKey) error {
			return nil // first contact: no pinned host key
		},
		Timeout: 2 * time.Second,
	}
	client, err := ssh.Dial("tcp", ln.Addr().String(), cfg)
	if err == nil {
		_ = client.Close()
		t.Fatal("auth must be rejected")
	}
	// x/crypto hides the server's rejection text, so the generic
	// "unable to authenticate" verdict is the observable proof.
	if !strings.Contains(err.Error(), "unable to authenticate") {
		t.Errorf("auth error %q does not look like an auth rejection", err)
	}
	select {
	case <-emuDone:
	case <-time.After(2 * time.Second):
		t.Error("emulator did not return after client disconnect")
	}
}

func TestSSHCapturesCredentials(t *testing.T) {
	emu, err := newSSHEmulator(2222)
	if err != nil {
		t.Fatalf("newSSHEmulator: %v", err)
	}
	events := make(chan event.Event, 4)
	emu.(CredentialReporter).SetReporter(func(ev event.Event) { events <- ev }, logsetup.Discard())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
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

	cfg := &ssh.ClientConfig{
		User: "root",
		Auth: []ssh.AuthMethod{ssh.Password("hunter2")},
		HostKeyCallback: func(string, net.Addr, ssh.PublicKey) error {
			return nil
		},
		Timeout: 2 * time.Second,
	}
	if client, err := ssh.Dial("tcp", ln.Addr().String(), cfg); err == nil {
		_ = client.Close()
		t.Fatal("auth must be rejected")
	}

	select {
	case ev := <-events:
		if ev.Kind != event.KindAuthAttempt {
			t.Errorf("kind = %q, want auth_attempt", ev.Kind)
		}
		if ev.Account != "root" || ev.Password != "hunter2" {
			t.Errorf("credentials = %q/%q, want root/hunter2", ev.Account, ev.Password)
		}
		if ev.DstPort != 2222 {
			t.Errorf("dst port = %d, want 2222", ev.DstPort)
		}
		if !ev.SrcIP.IsValid() {
			t.Error("src ip must be known for a TCP client")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no auth attempt event emitted")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("emulator did not return after client disconnect")
	}
}

func TestDefaultFactoryAllNames(t *testing.T) {
	for _, name := range []string{"silent", "http", "ftp", "telnet", "vnc", "mysql", "postgres", "ssh", "rdp"} {
		emu, err := defaultFactory(config.Trap{Port: 2222, Emulator: name})
		if err != nil {
			t.Errorf("factory(%q): %v", name, err)
			continue
		}
		if emu == nil {
			t.Errorf("factory(%q) returned nil", name)
		}
	}
	if _, err := defaultFactory(config.Trap{Emulator: "smtp"}); err == nil {
		t.Error("factory must reject unknown emulator names")
	}
}
