// Package clients holds what the clients of Notifications' neighbours share: the transport of every
// call to Edge's internal listener, which verifies the listener against the installation's
// certificate authority alone, never against the public roots (ADR 0039).
package clients

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/ancyloce/contelys-cloud-libs-service/telemetry"
)

// Authority is a certificate pool that trusts only the installation's authority in file.
func Authority(file string) (*x509.CertPool, error) {
	authority, err := os.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("clients: tls.authority: %w", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(authority) {
		return nil, errors.New("clients: tls.authority holds no certificate")
	}
	return roots, nil
}

// Transport is the transport of the calls to Edge's internal listener, verified against the
// authority in authorityFile, which carries each call's trace.
func Transport(authorityFile string) (http.RoundTripper, error) {
	roots, err := Authority(authorityFile)
	if err != nil {
		return nil, err
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	return telemetry.Transport(base), nil
}
