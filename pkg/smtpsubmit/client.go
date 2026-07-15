package smtpsubmit

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type TLSMode string

const (
	TLSNone     TLSMode = "none"
	TLSStartTLS TLSMode = "starttls"
	TLSImplicit TLSMode = "implicit"
)

type Config struct {
	Address, ServerName, ClientName string
	Username, Password              string
	TLSMode                         TLSMode
	RootCAs                         *x509.CertPool
	ConnectTimeout, CommandTimeout  time.Duration
}

type Request struct {
	EnvelopeFrom string
	Recipient    string
	Message      []byte
}

type Error struct {
	Stage     string
	Code      int
	Temporary bool
	Err       error
}

func (e *Error) Error() string {
	if e.Code != 0 {
		return fmt.Sprintf("SMTP %s: server returned %d: %v", e.Stage, e.Code, e.Err)
	}
	return fmt.Sprintf("SMTP %s: %v", e.Stage, e.Err)
}
func (e *Error) Unwrap() error { return e.Err }

func IsTemporary(err error) bool {
	var submitErr *Error
	return errors.As(err, &submitErr) && submitErr.Temporary
}

func Submit(ctx context.Context, config Config, request Request) error {
	config = withDefaults(config)
	if err := validateConfig(config); err != nil {
		return &Error{Stage: "config", Err: err}
	}
	if err := validateRequest(request); err != nil {
		return &Error{Stage: "request", Err: err}
	}
	dialer := net.Dialer{Timeout: config.ConnectTimeout}
	connection, err := dialer.DialContext(ctx, "tcp", config.Address)
	if err != nil {
		return &Error{Stage: "connect", Temporary: true, Err: err}
	}
	defer connection.Close()
	client := &client{connection: connection, timeout: config.CommandTimeout}
	if config.TLSMode == TLSImplicit {
		if err := client.startTLS(ctx, config); err != nil {
			return err
		}
	}
	client.resetIO()
	if err := client.expect("greeting", 220); err != nil {
		return err
	}
	capabilities, err := client.ehlo(config.ClientName)
	if err != nil {
		return err
	}
	if config.TLSMode == TLSStartTLS {
		if _, ok := capabilities["STARTTLS"]; !ok {
			return &Error{Stage: "starttls", Err: errors.New("relay did not advertise STARTTLS")}
		}
		if err := client.command("starttls", []int{220}, "STARTTLS"); err != nil {
			return err
		}
		if err := client.startTLS(ctx, config); err != nil {
			return err
		}
		client.resetIO()
		capabilities, err = client.ehlo(config.ClientName)
		if err != nil {
			return err
		}
	}
	if config.Username != "" {
		if !capabilityContains(capabilities, "AUTH", "PLAIN") {
			return &Error{Stage: "auth", Err: errors.New("relay did not advertise AUTH PLAIN")}
		}
		payload := base64.StdEncoding.EncodeToString([]byte("\x00" + config.Username + "\x00" + config.Password))
		if err := client.command("auth", []int{235}, "AUTH PLAIN "+payload); err != nil {
			return err
		}
	}
	if err := client.command("mail_from", []int{250}, "MAIL FROM:<"+request.EnvelopeFrom+">"); err != nil {
		return err
	}
	if err := client.command("rcpt_to", []int{250, 251}, "RCPT TO:<"+request.Recipient+">"); err != nil {
		return err
	}
	if err := client.command("data", []int{354}, "DATA"); err != nil {
		return err
	}
	if err := client.writeMessage(request.Message); err != nil {
		return &Error{Stage: "data_write", Temporary: true, Err: err}
	}
	if err := client.expect("data_result", 250); err != nil {
		return err
	}
	_ = client.command("quit", []int{221}, "QUIT")
	return nil
}

// ValidateConfig validates the closed smarthost contract without opening a
// connection. Callers can therefore fail before claiming an outbox entry.
func ValidateConfig(config Config) error { return validateConfig(withDefaults(config)) }

type client struct {
	connection net.Conn
	reader     *bufio.Reader
	timeout    time.Duration
}

func (c *client) resetIO() { c.reader = bufio.NewReaderSize(c.connection, 8192) }

func (c *client) startTLS(ctx context.Context, config Config) error {
	tlsConnection := tls.Client(c.connection, &tls.Config{ServerName: config.ServerName, RootCAs: config.RootCAs, MinVersion: tls.VersionTLS12})
	deadline := time.Now().Add(config.CommandTimeout)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	if err := tlsConnection.SetDeadline(deadline); err != nil {
		return &Error{Stage: "tls", Temporary: true, Err: err}
	}
	if err := tlsConnection.HandshakeContext(ctx); err != nil {
		return &Error{Stage: "tls", Temporary: isTransientTLSError(err), Err: err}
	}
	_ = tlsConnection.SetDeadline(time.Time{})
	c.connection = tlsConnection
	return nil
}

func (c *client) ehlo(name string) (map[string]string, error) {
	if err := c.writeLine("EHLO " + name); err != nil {
		return nil, &Error{Stage: "ehlo", Temporary: true, Err: err}
	}
	code, lines, err := c.readResponse()
	if err != nil {
		return nil, &Error{Stage: "ehlo", Temporary: isTransientResponseError(err), Err: err}
	}
	if code != 250 {
		return nil, responseError("ehlo", code, lines)
	}
	result := make(map[string]string)
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) > 0 {
			result[strings.ToUpper(fields[0])] = strings.Join(fields[1:], " ")
		}
	}
	return result, nil
}

func (c *client) command(stage string, expected []int, line string) error {
	if strings.ContainsAny(line, "\r\n") {
		return &Error{Stage: stage, Err: errors.New("SMTP command contains newline")}
	}
	if err := c.writeLine(line); err != nil {
		return &Error{Stage: stage, Temporary: true, Err: err}
	}
	code, lines, err := c.readResponse()
	if err != nil {
		return &Error{Stage: stage, Temporary: isTransientResponseError(err), Err: err}
	}
	for _, allowed := range expected {
		if code == allowed {
			return nil
		}
	}
	return responseError(stage, code, lines)
}

func (c *client) expect(stage string, expected int) error {
	code, lines, err := c.readResponse()
	if err != nil {
		return &Error{Stage: stage, Temporary: isTransientResponseError(err), Err: err}
	}
	if code != expected {
		return responseError(stage, code, lines)
	}
	return nil
}

func (c *client) writeLine(line string) error {
	if err := c.connection.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return err
	}
	_, err := c.connection.Write([]byte(line + "\r\n"))
	return err
}

func (c *client) readResponse() (int, []string, error) {
	if err := c.connection.SetReadDeadline(time.Now().Add(c.timeout)); err != nil {
		return 0, nil, err
	}
	var code int
	var lines []string
	for count := 0; count < 64; count++ {
		raw, err := c.reader.ReadSlice('\n')
		if err != nil {
			if errors.Is(err, bufio.ErrBufferFull) {
				return 0, nil, &protocolError{message: "oversized SMTP response line"}
			}
			return 0, nil, err
		}
		if len(raw) > 8192 || len(raw) < 6 || raw[len(raw)-2] != '\r' {
			return 0, nil, &protocolError{message: "malformed or oversized SMTP response"}
		}
		line := string(raw[:len(raw)-2])
		parsed, err := strconv.Atoi(line[:3])
		if err != nil || (line[3] != ' ' && line[3] != '-') {
			return 0, nil, &protocolError{message: "malformed SMTP response code"}
		}
		if count == 0 {
			code = parsed
		} else if parsed != code {
			return 0, nil, &protocolError{message: "inconsistent multiline SMTP response"}
		}
		lines = append(lines, line[4:])
		if line[3] == ' ' {
			return code, lines, nil
		}
	}
	return 0, nil, &protocolError{message: "too many SMTP response lines"}
}

func (c *client) writeMessage(message []byte) error {
	if len(message) == 0 || len(message) > 256*1024 {
		return errors.New("message size outside allowed range")
	}
	if bytesContainsBareNewline(message) {
		return errors.New("message contains bare CR or LF")
	}
	if err := c.connection.SetWriteDeadline(time.Now().Add(c.timeout)); err != nil {
		return err
	}
	message = bytes.TrimSuffix(message, []byte("\r\n"))
	for _, line := range bytes.Split(message, []byte("\r\n")) {
		if len(line) > 0 && line[0] == '.' {
			if _, err := c.connection.Write([]byte{'.'}); err != nil {
				return err
			}
		}
		if _, err := c.connection.Write(line); err != nil {
			return err
		}
		if _, err := c.connection.Write([]byte("\r\n")); err != nil {
			return err
		}
	}
	_, err := c.connection.Write([]byte(".\r\n"))
	return err
}

type protocolError struct{ message string }

func (e *protocolError) Error() string { return e.message }

func isTransientResponseError(err error) bool {
	var protocol *protocolError
	return !errors.As(err, &protocol)
}

func isTransientTLSError(err error) bool {
	var unknownAuthority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalidCertificate x509.CertificateInvalidError
	var recordHeader tls.RecordHeaderError
	if errors.As(err, &unknownAuthority) || errors.As(err, &hostname) || errors.As(err, &invalidCertificate) || errors.As(err, &recordHeader) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return true
	}
	// Preserve the one-time delivery material for unknown handshake failures.
	// Only recognized certificate/protocol configuration errors are permanent.
	return true
}

func responseError(stage string, code int, lines []string) error {
	return &Error{Stage: stage, Code: code, Temporary: code >= 400 && code < 500, Err: errors.New(strings.Join(lines, " / "))}
}

func capabilityContains(capabilities map[string]string, key, value string) bool {
	for _, item := range strings.Fields(capabilities[key]) {
		if strings.EqualFold(item, value) {
			return true
		}
	}
	return false
}

func withDefaults(config Config) Config {
	if config.ClientName == "" {
		config.ClientName = "filees.local"
	}
	if config.ConnectTimeout <= 0 {
		config.ConnectTimeout = 10 * time.Second
	}
	if config.CommandTimeout <= 0 {
		config.CommandTimeout = 30 * time.Second
	}
	return config
}

func validateConfig(config Config) error {
	host, _, err := net.SplitHostPort(config.Address)
	if err != nil {
		return errors.New("SMTP address must be host:port")
	}
	if !validAtom(config.ClientName) {
		return errors.New("invalid SMTP client name")
	}
	switch config.TLSMode {
	case TLSNone:
		ip := net.ParseIP(host)
		if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
			return errors.New("plaintext SMTP is allowed only on loopback")
		}
	case TLSStartTLS, TLSImplicit:
		if strings.TrimSpace(config.ServerName) == "" {
			return errors.New("SMTP TLS server_name is required")
		}
	default:
		return fmt.Errorf("unsupported SMTP TLS mode %q", config.TLSMode)
	}
	if config.Username != "" && (config.TLSMode == TLSNone || config.Password == "") {
		return errors.New("SMTP authentication requires TLS and a password")
	}
	if strings.ContainsAny(config.Username+config.Password, "\x00\r\n") {
		return errors.New("SMTP credentials contain forbidden characters")
	}
	return nil
}

func validateRequest(request Request) error {
	if !validMailbox(request.EnvelopeFrom) || !validMailbox(request.Recipient) {
		return errors.New("invalid SMTP envelope address")
	}
	if bytesContainsBareNewline(request.Message) {
		return errors.New("message contains bare CR or LF")
	}
	return nil
}

func validMailbox(value string) bool {
	return value != "" && len(value) <= 254 && strings.Count(value, "@") == 1 && !strings.ContainsAny(value, "<>\r\n \t")
}
func validAtom(value string) bool {
	return value != "" && len(value) <= 255 && !strings.ContainsAny(value, "\r\n \t")
}
func bytesContainsBareNewline(message []byte) bool {
	for index, char := range message {
		if char == '\n' && (index == 0 || message[index-1] != '\r') {
			return true
		}
		if char == '\r' && (index+1 >= len(message) || message[index+1] != '\n') {
			return true
		}
	}
	return false
}
