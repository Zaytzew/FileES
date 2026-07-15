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
	"fmt"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

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
