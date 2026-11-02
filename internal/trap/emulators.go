package trap

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"siren/internal/config"
)

// Deadline phases for trap dialogs. Real services answer banners within
// seconds; a scanner hanging on an open connection is dropped politely.
const (
	bannerTimeout = 5 * time.Second
	dialogTimeout = 30 * time.Second
)

// errClientClosed marks a clean disconnect by the client side; it is not an
// agent malfunction and must not be logged as an error by the manager.
var errClientClosed = errors.New("client closed connection")

// defaultFactory maps configured emulator names to implementations. The
// config package validates names, so an unknown name is a programming error.
// The full trap config is passed because the SSH emulator stamps the trap
// port onto the auth attempts it emits.
func defaultFactory(t config.Trap) (Emulator, error) {
	switch t.Emulator {
	case config.EmulatorSilent:
		return EmulatorSilentT{}, nil
	case config.EmulatorHTTP:
		return EmulatorHTTPT{}, nil
	case config.EmulatorFTP:
		return EmulatorFTPT{}, nil
	case config.EmulatorTelnet:
		return EmulatorTelnetT{}, nil
	case config.EmulatorVNC:
		return EmulatorVNCT{}, nil
	case config.EmulatorMySQL:
		return EmulatorMySQLT{}, nil
	case config.EmulatorPostgres:
		return EmulatorPostgresT{}, nil
	case config.EmulatorSSH:
		return newSSHEmulator(t.Port)
	case config.EmulatorRDP:
		return EmulatorRDPT{}, nil
	}
	return nil, fmt.Errorf("trap: no emulator implementation for %q", t.Emulator)
}

// readLine reads one CRLF/LF-terminated line with a deadline.
func readLine(conn net.Conn, timeout time.Duration) (string, error) {
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return "", err
	}
	r := bufio.NewReader(conn)
	line, err := r.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) {
			return "", errClientClosed
		}
		return "", err
	}
	return trimCRLF(line), nil
}

// writeAll sends data under a deadline.
func writeAll(conn net.Conn, timeout time.Duration, data []byte) error {
	if err := conn.SetWriteDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	_, err := conn.Write(data)
	return err
}

func trimCRLF(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// drainUntilClose reads and discards everything until the client disconnects
// or ctx is done; keeps "silent" and post-dialog phases realistic.
func drainUntilClose(ctx context.Context, conn net.Conn) {
	go func() {
		<-ctx.Done()
		_ = conn.SetReadDeadline(time.Now().Add(time.Millisecond))
	}()
	if err := conn.SetReadDeadline(time.Now().Add(dialogTimeout)); err != nil {
		return
	}
	buf := make([]byte, 4096)
	for {
		if _, err := conn.Read(buf); err != nil {
			return
		}
	}
}

// EmulatorSilentT accepts the connection and stays silent until the client
// goes away: looks like a filtered/quiet port to banner-grabbers.
type EmulatorSilentT struct{}

// Handle implements Emulator.
func (EmulatorSilentT) Handle(ctx context.Context, conn net.Conn) error {
	drainUntilClose(ctx, conn)
	return nil
}

// EmulatorHTTPT serves a plain default page, as an unconfigured web server
// would. The legend (server product, page text) is fixed for now; making it
// configurable is deferred until field tests show the need.
type EmulatorHTTPT struct{}

const httpBody = "<html>\r\n<head><title>Welcome!</title></head>\r\n" +
	"<body>\r\n<h1>Welcome!</h1>\r\n<p>If you see this page, the web server " +
	"is successfully installed and working.</p>\r\n</body>\r\n</html>\r\n"

// Handle implements Emulator.
func (EmulatorHTTPT) Handle(ctx context.Context, conn net.Conn) error {
	// A request may arrive in several packets; read headers up to the blank
	// line, tolerating clients that just banner-grab and hang up.
	if err := conn.SetReadDeadline(time.Now().Add(bannerTimeout)); err != nil {
		return err
	}
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errClientClosed
			}
			return err
		}
		if line == "\r\n" || line == "\n" {
			break
		}
	}
	resp := "HTTP/1.1 200 OK\r\n" +
		"Server: nginx/1.24.0\r\n" +
		"Content-Type: text/html\r\n" +
		"Content-Length: " + strconv.Itoa(len(httpBody)) + "\r\n" +
		"Connection: close\r\n" +
		"\r\n" +
		httpBody
	if err := writeAll(conn, bannerTimeout, []byte(resp)); err != nil {
		return err
	}
	drainUntilClose(ctx, conn)
	return nil
}

// EmulatorFTPT emulates a classic FTP login dialog that always fails.
type EmulatorFTPT struct{}

// Handle implements Emulator.
func (EmulatorFTPT) Handle(ctx context.Context, conn net.Conn) error {
	if err := writeAll(conn, bannerTimeout, []byte("220 ftp.local FTP server (Version 6.00LS) ready.\r\n")); err != nil {
		return err
	}
	for {
		line, err := readLine(conn, dialogTimeout)
		if err != nil {
			return err
		}
		cmd, arg := splitFTP(line)
		switch cmd {
		case "USER":
			if arg == "" {
				if err := writeAll(conn, dialogTimeout, []byte("501 Username required.\r\n")); err != nil {
					return err
				}
				continue
			}
			if err := writeAll(conn, dialogTimeout, []byte("331 Password required for "+arg+".\r\n")); err != nil {
				return err
			}
		case "PASS":
			if err := writeAll(conn, dialogTimeout, []byte("530 Login incorrect.\r\n")); err != nil {
				return err
			}
		case "QUIT":
			if err := writeAll(conn, dialogTimeout, []byte("221 Goodbye.\r\n")); err != nil {
				return err
			}
			return nil
		default:
			if err := writeAll(conn, dialogTimeout, []byte("502 Command not implemented.\r\n")); err != nil {
				return err
			}
		}
	}
}

func splitFTP(line string) (cmd, arg string) {
	for i := 0; i < len(line); i++ {
		if line[i] == ' ' {
			return line[:i], line[i+1:]
		}
	}
	return line, ""
}

// EmulatorTelnetT emulates a text login prompt that always fails.
type EmulatorTelnetT struct{}

// Handle implements Emulator.
func (EmulatorTelnetT) Handle(ctx context.Context, conn net.Conn) error {
	if err := writeAll(conn, bannerTimeout, []byte("Welcome to Ubuntu 22.04 LTS (GNU/Linux 5.15.0-91-generic x86_64)\r\n\r\nlogin: ")); err != nil {
		return err
	}
	if _, err := readLine(conn, dialogTimeout); err != nil {
		return err
	}
	if err := writeAll(conn, dialogTimeout, []byte("Password: ")); err != nil {
		return err
	}
	if _, err := readLine(conn, dialogTimeout); err != nil {
		return err
	}
	if err := writeAll(conn, dialogTimeout, []byte("Login incorrect\r\n\r\nlogin: ")); err != nil {
		return err
	}
	_, _ = readLine(conn, dialogTimeout)
	drainUntilClose(ctx, conn)
	return nil
}

// EmulatorVNCT emulates the RFB handshake up to a minimal ServerInit, then
// goes idle until the client disconnects.
type EmulatorVNCT struct{}

// Handle implements Emulator.
func (EmulatorVNCT) Handle(ctx context.Context, conn net.Conn) error {
	// Protocol version exchange.
	if err := writeAll(conn, bannerTimeout, []byte("RFB 003.008\n")); err != nil {
		return err
	}
	ver := make([]byte, 12)
	if err := readFull(conn, bannerTimeout, ver); err != nil {
		return err
	}
	// Security types: exactly one type, None (1).
	if err := writeAll(conn, bannerTimeout, []byte{1, 1}); err != nil {
		return err
	}
	choice := make([]byte, 1)
	if err := readFull(conn, dialogTimeout, choice); err != nil {
		return err
	}
	if choice[0] != 1 {
		// Unknown security type: real servers just drop the connection.
		return fmt.Errorf("vnc: unexpected security type %d", choice[0])
	}
	// SecurityResult: OK.
	if err := writeAll(conn, dialogTimeout, []byte{0, 0, 0, 0}); err != nil {
		return err
	}
	// ClientInit: shared flag.
	init := make([]byte, 1)
	if err := readFull(conn, dialogTimeout, init); err != nil {
		return err
	}
	// ServerInit: 1024x768, 32bpp, minimal pixel format, name "ubuntu".
	si := make([]byte, 0, 24+16+4+6)
	si = appendU16(si, 1024)      // width
	si = appendU16(si, 768)       // height
	si = append(si, 32, 24, 0, 1) // bits-per-pixel, depth, big-endian, true-colour
	si = appendU16(si, 255)       // red-max
	si = appendU16(si, 255)       // green-max
	si = appendU16(si, 255)       // blue-max
	si = append(si, 16, 8, 0)     // red/green/blue shift
	si = append(si, 0, 0, 0)      // padding
	si = appendU32(si, 6)         // name length
	si = append(si, []byte("ubuntu")...)
	if err := writeAll(conn, dialogTimeout, si); err != nil {
		return err
	}
	drainUntilClose(ctx, conn)
	return nil
}

func appendU16(b []byte, v uint16) []byte {
	return binary.BigEndian.AppendUint16(b, v)
}

func appendU32(b []byte, v uint32) []byte {
	return binary.BigEndian.AppendUint32(b, v)
}

// readFull reads exactly len(buf) bytes under a deadline.
func readFull(conn net.Conn, timeout time.Duration, buf []byte) error {
	if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	_, err := io.ReadFull(conn, buf)
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return errClientClosed
	}
	return err
}

// EmulatorMySQLT sends a MySQL 8 native-password handshake greeting, then
// answers the login response with an access-denied error packet.
type EmulatorMySQLT struct{}

// Handle implements Emulator.
func (EmulatorMySQLT) Handle(ctx context.Context, conn net.Conn) error {
	if err := writeAll(conn, bannerTimeout, mysqlGreeting()); err != nil {
		return err
	}
	// Wait for the client's handshake response (any bytes), then deny.
	buf := make([]byte, 4096)
	if err := conn.SetReadDeadline(time.Now().Add(dialogTimeout)); err != nil {
		return err
	}
	if _, err := conn.Read(buf); err != nil {
		if errors.Is(err, io.EOF) {
			return errClientClosed
		}
		return err
	}
	if err := writeAll(conn, dialogTimeout, mysqlAuthError()); err != nil {
		return err
	}
	drainUntilClose(ctx, conn)
	return nil
}

// mysqlGreeting builds a protocol-41 initial handshake packet with a fixed
// salt; a fixed salt is fine because authentication always fails.
func mysqlGreeting() []byte {
	payload := []byte{10} // protocol version 10
	payload = append(payload, []byte("8.0.36")...)
	payload = append(payload, 0)
	payload = appendU32(payload, 1)                  // connection id
	payload = append(payload, []byte("SALT1234")...) // auth data part 1 (8)
	payload = append(payload, 0)
	payload = appendU16(payload, 0xa285)                   // capability flags (lower)
	payload = append(payload, 0xff)                        // charset utf8mb4
	payload = appendU16(payload, 2)                        // status flags
	payload = appendU16(payload, 0xc1ff)                   // capability flags (upper)
	payload = append(payload, 21)                          // auth data length (21)
	payload = append(payload, make([]byte, 10)...)         // reserved
	payload = append(payload, []byte("SALT5678901234")...) // auth data part 2 (12+1)
	payload = append(payload, 0)
	payload = append(payload, []byte("mysql_native_password")...)
	payload = append(payload, 0)
	return mysqlPacket(payload, 0)
}

// mysqlAuthError builds an ERR packet: access denied for user.
func mysqlAuthError() []byte {
	payload := []byte{0xff}            // ERR header
	payload = appendU16(payload, 1045) // error code ER_ACCESS_DENIED_ERROR
	payload = append(payload, '#')
	payload = append(payload, []byte("28000")...) // SQL state
	payload = append(payload, []byte("Access denied for user 'admin'@'%' (using password: YES)")...)
	return mysqlPacket(payload, 2)
}

// mysqlPacket frames a payload with the 4-byte MySQL header.
func mysqlPacket(payload []byte, seq byte) []byte {
	out := make([]byte, 0, len(payload)+4)
	out = appendU24(out, uint32(len(payload)))
	out = append(out, seq)
	return append(out, payload...)
}

func appendU24(b []byte, v uint32) []byte {
	return append(b, byte(v), byte(v>>8), byte(v>>16))
}

// EmulatorPostgresT requests a cleartext password like a real server, then
// reports an authentication failure.
type EmulatorPostgresT struct{}

// Handle implements Emulator.
func (EmulatorPostgresT) Handle(ctx context.Context, conn net.Conn) error {
	// AuthenticationCleartextPassword request.
	if err := writeAll(conn, bannerTimeout, []byte{'R', 0, 0, 0, 8, 0, 0, 0, 3}); err != nil {
		return err
	}
	// Read the client's PasswordMessage ('p' + total length + body) fully:
	// a half-read message would stall pipelined clients.
	header := make([]byte, 5)
	if err := readFull(conn, dialogTimeout, header); err != nil {
		return err
	}
	if header[0] == 0 { // SSLRequest or other oddity: real servers drop these
		return errClientClosed
	}
	bodyLen := int(binary.BigEndian.Uint32(header[1:5])) - 4
	if bodyLen > 0 {
		body := make([]byte, bodyLen)
		if err := readFull(conn, dialogTimeout, body); err != nil {
			return err
		}
	}
	if err := writeAll(conn, dialogTimeout, postgresAuthError()); err != nil {
		return err
	}
	drainUntilClose(ctx, conn)
	return nil
}

// postgresAuthError builds a FATAL ErrorResponse for a wrong password.
func postgresAuthError() []byte {
	payload := []byte{'E', 0, 0, 0, 0} // header patched below
	fields := []byte{}
	fields = append(fields, 'S')
	fields = append(fields, []byte("FATAL\x00")...)
	fields = append(fields, 'V')
	fields = append(fields, []byte("FATAL\x00")...)
	fields = append(fields, 'C')
	fields = append(fields, []byte("28P01\x00")...)
	fields = append(fields, 'M')
	fields = append(fields, []byte("password authentication failed for user \"admin\"\x00")...)
	fields = append(fields, 'R')
	fields = append(fields, []byte("auth.c\x00")...)
	fields = append(fields, 0)
	payload = append(payload, fields...)
	binary.BigEndian.PutUint32(payload[1:5], uint32(len(payload)))
	return payload
}

// EmulatorRDPT answers the X.224 connection request the way Windows RDP
// does, then goes silent until the client disconnects; a full RDP stack is
// out of scope, but banner-grabbers see a valid RDP negotiate response.
type EmulatorRDPT struct{}

// Handle implements Emulator.
func (EmulatorRDPT) Handle(ctx context.Context, conn net.Conn) error {
	// Read the X.224 Connection Request (TPKT header + length).
	hdr := make([]byte, 4)
	if err := readFull(conn, bannerTimeout, hdr); err != nil {
		return err
	}
	if hdr[0] != 3 { // TPKT version 3 expected
		return fmt.Errorf("rdp: unexpected TPKT version %d", hdr[0])
	}
	rest := make([]byte, int(binary.BigEndian.Uint16(hdr[2:4]))-4)
	if len(rest) > 0 {
		if err := readFull(conn, bannerTimeout, rest); err != nil {
			return err
		}
	}
	// X.224 Connection Confirm + RDP Negotiation Response (PROTOCOL_HYBRID).
	// TPKT(4) + LI(1) + CC code(1) + dst-ref(2) + src-ref(2) + class(1)
	// + negotiation(8) = 19 bytes.
	cc := []byte{
		3, 0, 0, 0x13, // TPKT: length 19
		0x0e,       // X.224 length indicator
		0xd0,       // CC code, class 0
		0x00, 0x00, // dst reference
		0x00, 0x00, // src reference
		0x00,                   // class
		0x02, 0x00, 0x08, 0x00, // TYPE_RDP_NEG_RSP, flags, length 8
		0x03, 0x00, 0x00, 0x00, // PROTOCOL_HYBRID (NLA required)
	}
	if err := writeAll(conn, bannerTimeout, cc); err != nil {
		return err
	}
	drainUntilClose(ctx, conn)
	return nil
}
