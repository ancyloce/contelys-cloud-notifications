package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/ancyloce/contelys-cloud-notifications/internal/infrastructure/clients"
)

// checkHealth asks the instance's own readiness endpoint, as Docker and Compose do through the
// image's health check, and exits 0 only when the instance is ready.
func checkHealth(ctx context.Context, args []string, out io.Writer) int {
	path, ok := configPath("health", args, out)
	if !ok {
		return 2
	}
	if err := askReadiness(ctx, path); err != nil {
		fmt.Fprintln(out, "not ready:", err)
		return 1
	}
	fmt.Fprintln(out, "ready")
	return 0
}

// listener is what the health check reads of the configuration file: the instance's listener. It
// reads nothing else and checks nothing else, because the file on disk may hold a change that the
// instance refused and does not apply; readiness answers for the configuration in force.
type listener struct {
	Listen string `yaml:"listen"`
	TLS    struct {
		Certificate string `yaml:"certificate"`
		Authority   string `yaml:"authority"`
	} `yaml:"tls"`
}

// askReadiness asks GET /readyz of the instance whose configuration file is at path. It verifies
// the listener's certificate against the installation's authority, under the first name that the
// certificate carries.
func askReadiness(ctx context.Context, path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var l listener
	if err := yaml.Unmarshal(content, &l); err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(l.Listen)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	roots, err := clients.Authority(l.TLS.Authority)
	if err != nil {
		return err
	}
	name, err := certificateName(l.TLS.Certificate)
	if err != nil {
		return err
	}
	client := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: name, MinVersion: tls.VersionTLS12},
	}}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+net.JoinHostPort("127.0.0.1", port)+"/readyz", nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("readiness answered %d", response.StatusCode)
	}
	return nil
}

// certificateName is the first DNS name of the certificate in file.
func certificateName(file string) (string, error) {
	content, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(content)
	if block == nil {
		return "", errors.New("tls.certificate holds no certificate")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", err
	}
	if len(certificate.DNSNames) == 0 {
		return "", errors.New("tls.certificate names no host")
	}
	return certificate.DNSNames[0], nil
}
