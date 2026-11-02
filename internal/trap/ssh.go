package trap

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net"
	"time"

	"golang.org/x/crypto/ssh"

	"siren/internal/event"
)

// sshEmulator performs a real SSH-2 handshake and always rejects
// authentication, exactly as a hardened real server would treat a wrong
// password. Banner-grabbers and brute-force tools see an authentic OpenSSH
// endpoint.
//
// Passwords offered by clients are captured before rejection: they are what
// an attacker types into the decoy, which is exactly the intelligence a
// honeypot exists for. Real-service doors never carry passwords — Windows
// does not expose the attempted password in 4625 events.
type sshEmulator struct {
	cfg *ssh.ServerConfig

	// emit and log are wired by the manager via SetReporter; a nil emit
	// (unit tests) only disables the extra auth-attempt event.
	emit func(event.Event)
	log  *slog.Logger

	port int
}

// CredentialReporter is implemented by emulators that observe client
// credentials during their dialog. The manager wires the event sink after
// construction.
type CredentialReporter interface {
	SetReporter(emit func(event.Event), log *slog.Logger)
}

// SetReporter implements CredentialReporter.
func (e *sshEmulator) SetReporter(emit func(event.Event), log *slog.Logger) {
	e.emit = emit
	e.log = log
}

// newSSHEmulator builds the server config with an ephemeral host key. A key
// per emulator instance is fine: persistence only matters if clients pin
// host keys, which first-time attackers do not. The port is stamped onto
// emitted auth events; the emulator itself does not need it for the
// handshake.
func newSSHEmulator(port int) (Emulator, error) {
	e := &sshEmulator{port: port}
	s, err := signer()
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, password []byte) (*ssh.Permissions, error) {
			e.recordAuth(conn, string(password))
			// The same verdict a real server gives for a wrong password.
			return nil, fmt.Errorf("password rejected for %q", conn.User())
		},
		// Public key auth is offered and always denied, like on a real
		// box without the attacker's key.
		PublicKeyCallback: func(_ ssh.ConnMetadata, _ ssh.PublicKey) (*ssh.Permissions, error) {
			return nil, fmt.Errorf("public key rejected")
		},
	}
	cfg.AddHostKey(s)
	e.cfg = cfg
	return e, nil
}

// signer is a helper keeping newSSHEmulator readable: it builds an ed25519
// host key signer.
func signer() (ssh.Signer, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("trap: ssh host key: %w", err)
	}
	s, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("trap: ssh signer: %w", err)
	}
	return s, nil
}

// recordAuth logs the attempt and, when wired, emits an auth_attempt fact
// with the offered credentials.
func (e *sshEmulator) recordAuth(conn ssh.ConnMetadata, password string) {
	if e.log != nil {
		e.log.Info("ssh trap: auth attempt recorded",
			"user", conn.User(),
			"password", password,
			"remote", conn.RemoteAddr().String(),
		)
	}
	if e.emit == nil {
		return
	}
	ev := event.Event{
		Kind:     event.KindAuthAttempt,
		Time:     time.Now(),
		Protocol: "ssh",
		DstPort:  e.port,
		Account:  conn.User(),
		Password: password,
	}
	if ta, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		ev.SrcIP = ta.AddrPort().Addr()
	}
	e.emit(ev)
}

// Handle implements Emulator. Handshake failures (bad versions, failed
// auth, disconnects) are normal brute-force traffic and surface as
// errClientClosed, not as emulator faults.
func (e *sshEmulator) Handle(ctx context.Context, conn net.Conn) error {
	// Bound the whole handshake+session by one deadline; a real server
	// drops clients that stall during key exchange.
	_ = conn.SetDeadline(time.Now().Add(dialogTimeout))
	sconn, chans, reqs, err := ssh.NewServerConn(conn, e.cfg)
	if err != nil {
		return errClientClosed
	}
	defer func() { _ = sconn.Close() }()
	go ssh.DiscardRequests(reqs)

	// No services are ever offered: every channel request is rejected the
	// way a server without matching subsystems does.
	go func() {
		for newCh := range chans {
			_ = newCh.Reject(ssh.Prohibited, "service unavailable")
		}
	}()
	// Wait until the client gives up and disconnects.
	_ = sconn.Wait()
	return nil
}
