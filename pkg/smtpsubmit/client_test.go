package smtpsubmit

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestMalformedAndOversizedResponsesArePermanentWithoutPanic(t *testing.T) {
	for _, test := range []struct {
		name     string
		response string
	}{
		{name: "missing separator", response: "250\r\n"},
		{name: "oversized", response: strings.Repeat("x", 9000) + "\r\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clientConnection, serverConnection := net.Pipe()
			defer clientConnection.Close()
			defer serverConnection.Close()
			go func() {
				_, _ = serverConnection.Write([]byte(test.response))
			}()
			c := &client{connection: clientConnection, reader: bufio.NewReaderSize(clientConnection, 8192), timeout: time.Second}
			err := c.expect("greeting", 220)
			if err == nil || IsTemporary(err) {
				t.Fatalf("error=%v temporary=%v", err, IsTemporary(err))
			}
			var submitErr *Error
			if !errors.As(err, &submitErr) || submitErr.Stage != "greeting" {
				t.Fatalf("unexpected error: %#v", err)
			}
		})
	}
}

func TestRequestValidationHasOwnStageAndWriteMessageRejectsBareNewline(t *testing.T) {
	err := Submit(context.Background(), Config{Address: "127.0.0.1:2525", ClientName: "filees.test", TLSMode: TLSNone}, Request{EnvelopeFrom: "filees@example.test", Recipient: "user@example.test", Message: []byte("bare\n")})
	var submitErr *Error
	if !errors.As(err, &submitErr) || submitErr.Stage != "request" || IsTemporary(err) {
		t.Fatalf("request validation error=%#v", err)
	}
	c := &client{}
	if err := c.writeMessage([]byte("bare\n")); err == nil {
		t.Fatal("writeMessage accepted bare LF")
	}
}

func TestTLSFailureClassificationPreservesTransientDeliveryMaterial(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, context.Canceled, io.EOF, io.ErrUnexpectedEOF, syscall.ECONNRESET, syscall.EPIPE, syscall.ETIMEDOUT} {
		if !isTransientTLSError(err) {
			t.Errorf("%T %v classified permanent", err, err)
		}
	}
	certificate := &x509.Certificate{}
	for _, err := range []error{
		x509.UnknownAuthorityError{Cert: certificate},
		x509.HostnameError{Certificate: certificate, Host: "wrong.example"},
		x509.CertificateInvalidError{Cert: certificate, Reason: x509.Expired},
		tls.RecordHeaderError{},
	} {
		if isTransientTLSError(err) {
			t.Errorf("%T classified temporary", err)
		}
	}
}

func TestStartTLSClassifiesTransportAndCertificateFailures(t *testing.T) {
	t.Run("transport EOF is temporary", func(t *testing.T) {
		clientConnection, serverConnection := net.Pipe()
		serverConnection.Close()
		c := &client{connection: clientConnection, timeout: time.Second}
		err := c.startTLS(context.Background(), Config{ServerName: "localhost", CommandTimeout: time.Second})
		clientConnection.Close()
		if err == nil || !IsTemporary(err) {
			t.Fatalf("TLS EOF error=%v temporary=%v", err, IsTemporary(err))
		}
	})

	t.Run("hostname mismatch is permanent", func(t *testing.T) {
		certificate, roots := testCertificate(t)
		clientConnection, serverConnection := net.Pipe()
		serverDone := make(chan struct{})
		go func() {
			defer close(serverDone)
			defer serverConnection.Close()
			_ = tls.Server(serverConnection, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}).Handshake()
		}()
		c := &client{connection: clientConnection, timeout: time.Second}
		err := c.startTLS(context.Background(), Config{ServerName: "wrong.example", RootCAs: roots, CommandTimeout: time.Second})
		clientConnection.Close()
		<-serverDone
		if err == nil || IsTemporary(err) {
			t.Fatalf("TLS hostname error=%v temporary=%v", err, IsTemporary(err))
		}
	})
}

func TestSubmitSTARTTLSAuthAndDotStuffing(t *testing.T) {
	certificate, roots := testCertificate(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	result := make(chan serverResult, 1)
	go serveTLSRelay(listener, certificate, result)

	message := []byte("From: <filees@example.net>\r\nTo: <user@example.net>\r\n\r\n.first\r\nlast\r\n")
	err = Submit(context.Background(), Config{Address: listener.Addr().String(), ServerName: "localhost", ClientName: "filees.test", Username: "filees", Password: "secret", TLSMode: TLSStartTLS, RootCAs: roots, CommandTimeout: 2 * time.Second}, Request{EnvelopeFrom: "filees@example.net", Recipient: "user@example.net", Message: message})
	if err != nil {
		t.Fatal(err)
	}
	got := <-result
	if got.err != nil {
		t.Fatal(got.err)
	}
	if !strings.Contains(got.commands, "AUTH PLAIN AGZpbGVlcwBzZWNyZXQ=\r\n") {
		t.Fatalf("AUTH exchange missing: %q", got.commands)
	}
	if !strings.Contains(got.message, "..first\r\n") {
		t.Fatalf("DATA was not dot-stuffed: %q", got.message)
	}
}

func TestSubmitSTARTTLSAuthLoginFallback(t *testing.T) {
	certificate, roots := testCertificate(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	result := make(chan serverResult, 1)
	go serveTLSLoginRelay(listener, certificate, result)

	err = Submit(context.Background(), Config{Address: listener.Addr().String(), ServerName: "localhost", ClientName: "filees.test", Username: "filees", Password: "secret", TLSMode: TLSStartTLS, RootCAs: roots, CommandTimeout: 2 * time.Second}, Request{EnvelopeFrom: "filees@example.net", Recipient: "user@example.net", Message: []byte("From: <filees@example.net>\r\n\r\ntest\r\n")})
	if err != nil {
		t.Fatal(err)
	}
	got := <-result
	if got.err != nil {
		t.Fatal(got.err)
	}
	for _, command := range []string{"AUTH LOGIN\r\n", "ZmlsZWVz\r\n", "c2VjcmV0\r\n"} {
		if !strings.Contains(got.commands, command) {
			t.Fatalf("AUTH LOGIN exchange missing %q in %q", command, got.commands)
		}
	}
	if strings.Contains(got.commands, "AUTH PLAIN") {
		t.Fatalf("used AUTH PLAIN despite LOGIN-only relay: %q", got.commands)
	}
}

func TestSubmitClassifiesRelayErrorsAndRejectsUnsafePlaintext(t *testing.T) {
	for _, test := range []struct {
		code      int
		temporary bool
	}{{450, true}, {550, false}} {
		t.Run(fmt.Sprint(test.code), func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go serveRejectingRelay(listener, test.code)
			err = Submit(context.Background(), Config{Address: listener.Addr().String(), ClientName: "filees.test", TLSMode: TLSNone, CommandTimeout: time.Second}, Request{EnvelopeFrom: "filees@example.net", Recipient: "user@example.net", Message: []byte("From: <filees@example.net>\r\n\r\ntest\r\n")})
			if err == nil || IsTemporary(err) != test.temporary {
				t.Fatalf("error=%v temporary=%v, want %v", err, IsTemporary(err), test.temporary)
			}
		})
	}
	err := Submit(context.Background(), Config{Address: "smtp.example.net:25", ClientName: "filees.test", TLSMode: TLSNone}, Request{EnvelopeFrom: "filees@example.net", Recipient: "user@example.net", Message: []byte("x\r\n")})
	if err == nil || IsTemporary(err) {
		t.Fatalf("unsafe plaintext error=%v", err)
	}
}

type serverResult struct {
	commands string
	message  string
	err      error
}

func serveTLSRelay(listener net.Listener, certificate tls.Certificate, result chan<- serverResult) {
	connection, err := listener.Accept()
	if err != nil {
		result <- serverResult{err: err}
		return
	}
	defer connection.Close()
	commands := &strings.Builder{}
	reader := bufio.NewReader(connection)
	write := func(line string) error { _, err := fmt.Fprintf(connection, "%s\r\n", line); return err }
	read := func() (string, error) {
		line, err := reader.ReadString('\n')
		commands.WriteString(line)
		return line, err
	}
	if err := write("220 relay.test ESMTP"); err != nil {
		result <- serverResult{err: err}
		return
	}
	if _, err := read(); err != nil {
		result <- serverResult{err: err}
		return
	}
	if _, err := fmt.Fprint(connection, "250-relay.test\r\n250-STARTTLS\r\n250 AUTH PLAIN\r\n"); err != nil {
		result <- serverResult{err: err}
		return
	}
	if line, err := read(); err != nil || line != "STARTTLS\r\n" {
		result <- serverResult{err: fmt.Errorf("STARTTLS command=%q err=%v", line, err)}
		return
	}
	if err := write("220 ready"); err != nil {
		result <- serverResult{err: err}
		return
	}
	tlsConnection := tls.Server(connection, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	if err := tlsConnection.Handshake(); err != nil {
		result <- serverResult{err: err}
		return
	}
	connection, reader = tlsConnection, bufio.NewReader(tlsConnection)
	if _, err := read(); err != nil {
		result <- serverResult{err: err}
		return
	}
	if _, err := fmt.Fprint(connection, "250-relay.test\r\n250 AUTH PLAIN\r\n"); err != nil {
		result <- serverResult{err: err}
		return
	}
	for _, response := range []string{"235 authenticated", "250 sender ok", "250 recipient ok", "354 end with dot"} {
		if _, err := read(); err != nil {
			result <- serverResult{err: err}
			return
		}
		if err := write(response); err != nil {
			result <- serverResult{err: err}
			return
		}
	}
	message := &strings.Builder{}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			result <- serverResult{err: err}
			return
		}
		if line == ".\r\n" {
			break
		}
		message.WriteString(line)
	}
	if err := write("250 queued"); err != nil {
		result <- serverResult{err: err}
		return
	}
	if _, err := read(); err == nil {
		_ = write("221 bye")
	}
	result <- serverResult{commands: commands.String(), message: message.String()}
}

func serveTLSLoginRelay(listener net.Listener, certificate tls.Certificate, result chan<- serverResult) {
	connection, err := listener.Accept()
	if err != nil {
		result <- serverResult{err: err}
		return
	}
	defer connection.Close()
	commands := &strings.Builder{}
	reader := bufio.NewReader(connection)
	write := func(line string) error { _, err := fmt.Fprintf(connection, "%s\r\n", line); return err }
	read := func() (string, error) {
		line, err := reader.ReadString('\n')
		commands.WriteString(line)
		return line, err
	}
	if err := write("220 relay.test ESMTP"); err != nil {
		result <- serverResult{err: err}
		return
	}
	if _, err := read(); err != nil {
		result <- serverResult{err: err}
		return
	}
	if _, err := fmt.Fprint(connection, "250-relay.test\r\n250 STARTTLS\r\n"); err != nil {
		result <- serverResult{err: err}
		return
	}
	if line, err := read(); err != nil || line != "STARTTLS\r\n" {
		result <- serverResult{err: fmt.Errorf("STARTTLS command=%q err=%v", line, err)}
		return
	}
	if err := write("220 ready"); err != nil {
		result <- serverResult{err: err}
		return
	}
	tlsConnection := tls.Server(connection, &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	if err := tlsConnection.Handshake(); err != nil {
		result <- serverResult{err: err}
		return
	}
	connection, reader = tlsConnection, bufio.NewReader(tlsConnection)
	if _, err := read(); err != nil {
		result <- serverResult{err: err}
		return
	}
	if _, err := fmt.Fprint(connection, "250-relay.test\r\n250 AUTH LOGIN\r\n"); err != nil {
		result <- serverResult{err: err}
		return
	}
	for _, exchange := range []struct {
		want, reply string
	}{
		{"AUTH LOGIN\r\n", "334 VXNlcm5hbWU6"},
		{"ZmlsZWVz\r\n", "334 UGFzc3dvcmQ6"},
		{"c2VjcmV0\r\n", "235 authenticated"},
		{"MAIL FROM:<filees@example.net>\r\n", "250 sender ok"},
		{"RCPT TO:<user@example.net>\r\n", "250 recipient ok"},
		{"DATA\r\n", "354 end with dot"},
	} {
		line, err := read()
		if err != nil || line != exchange.want {
			result <- serverResult{err: fmt.Errorf("command=%q want=%q err=%v", line, exchange.want, err)}
			return
		}
		if err := write(exchange.reply); err != nil {
			result <- serverResult{err: err}
			return
		}
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			result <- serverResult{err: err}
			return
		}
		if line == ".\r\n" {
			break
		}
	}
	if err := write("250 queued"); err != nil {
		result <- serverResult{err: err}
		return
	}
	if _, err := read(); err == nil {
		_ = write("221 bye")
	}
	result <- serverResult{commands: commands.String()}
}

func serveRejectingRelay(listener net.Listener, code int) {
	connection, err := listener.Accept()
	if err != nil {
		return
	}
	defer connection.Close()
	reader := bufio.NewReader(connection)
	fmt.Fprint(connection, "220 relay.test ESMTP\r\n")
	reader.ReadString('\n')
	fmt.Fprint(connection, "250 relay.test\r\n")
	reader.ReadString('\n')
	fmt.Fprint(connection, "250 sender ok\r\n")
	reader.ReadString('\n')
	fmt.Fprintf(connection, "%d recipient rejected\r\n", code)
}

func testCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"}, DNSNames: []string{"localhost"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(parsed)
	return certificate, pool
}
