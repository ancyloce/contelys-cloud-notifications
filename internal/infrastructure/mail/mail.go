// Package mail is the only code of Notifications that talks to the mail server (spec, "Modules").
// It reaches one configured server over TLS, through STARTTLS or implicitly, and verifies the
// server's certificate against the public roots that the image carries and, where smtp.ca names
// one, the installation's own authority. Plain SMTP happens only when smtp.tls says none.
package mail

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"time"

	gomail "github.com/wneessen/go-mail"
)

// TLSMode is how the connection to the mail server is encrypted: smtp.tls.
type TLSMode string

const (
	StartTLS TLSMode = "starttls"
	Implicit TLSMode = "implicit"
	// None is plain SMTP, which a signal and the installation's checks report as a warning.
	None TLSMode = "none"
)

// ParseTLSMode reads smtp.tls. It has no default, so plain SMTP is only ever chosen explicitly.
func ParseTLSMode(s string) (TLSMode, error) {
	switch mode := TLSMode(s); mode {
	case StartTLS, Implicit, None:
		return mode, nil
	}
	return "", errors.New("smtp.tls must be starttls, implicit or none")
}

// Server is the configured mail server.
type Server struct {
	Host string
	Port int
	TLS  TLSMode
	// Roots verify the server's certificate.
	Roots *x509.CertPool
	// Connect and Command bound the connection and each SMTP command.
	Connect, Command time.Duration
}

// Roots are the public roots of the image, with the authority in caFile added when it names one.
func Roots(caFile string) (*x509.CertPool, error) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("mail: the public roots: %w", err)
	}
	if caFile == "" {
		return roots, nil
	}
	authority, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("mail: smtp.ca: %w", err)
	}
	if !roots.AppendCertsFromPEM(authority) {
		return nil, errors.New("mail: smtp.ca holds no certificate")
	}
	return roots, nil
}

// Reachable connects to the mail server, negotiates TLS as its mode says, verifying the server's
// certificate, and quits without sending anything.
func (s Server) Reachable(ctx context.Context) error {
	client, err := s.client()
	if err != nil {
		return err
	}
	dial, cancel := context.WithTimeout(ctx, s.Connect+s.Command)
	defer cancel()
	if err := client.DialWithContext(dial); err != nil {
		return fmt.Errorf("mail: %w", err)
	}
	return client.Close()
}

// client is a go-mail client of the server. go-mail never sends again on its own.
func (s Server) client() (*gomail.Client, error) {
	options := []gomail.Option{
		gomail.WithPort(s.Port),
		gomail.WithTimeout(s.Command),
		gomail.WithTLSConfig(&tls.Config{RootCAs: s.Roots, ServerName: s.Host, MinVersion: tls.VersionTLS12}),
	}
	switch s.TLS {
	case StartTLS:
		options = append(options, gomail.WithTLSPolicy(gomail.TLSMandatory))
	case Implicit:
		options = append(options, gomail.WithSSL())
	case None:
		options = append(options, gomail.WithTLSPolicy(gomail.NoTLS))
	default:
		return nil, errors.New("mail: no TLS mode")
	}
	client, err := gomail.NewClient(s.Host, options...)
	if err != nil {
		return nil, fmt.Errorf("mail: %w", err)
	}
	return client, nil
}
