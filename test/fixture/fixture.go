// Package fixture holds what Notifications' tests share: a throwaway certificate authority, created
// in memory. Its keys are written only into the containers and temporary directories of a test
// run, never into the repository, because lint scans the project directory with gitleaks
// (Notifications ADR 0002).
package fixture

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"time"
)

// Authority is a throwaway certificate authority.
type Authority struct {
	certificate *x509.Certificate
	key         *ecdsa.PrivateKey
	// PEM is the authority's certificate, which a trust store holds.
	PEM []byte
}

// Certificate is a server certificate that the authority issued, as PEM and as a tls.Certificate.
type Certificate struct {
	PEM    []byte
	KeyPEM []byte
	Pair   tls.Certificate
}

// NewAuthority generates a certificate authority that lives for one day.
func NewAuthority() (*Authority, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "Notifications test certificate authority"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &Authority{certificate: certificate, key: key, PEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}, nil
}

// Issue returns a server certificate for the given names.
func (a *Authority) Issue(names ...string) (Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return Certificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, a.certificate, &key.PublicKey, a.key)
	if err != nil {
		return Certificate{}, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return Certificate{}, err
	}
	issued := Certificate{
		PEM:    pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
	}
	issued.Pair, err = tls.X509KeyPair(issued.PEM, issued.KeyPEM)
	return issued, err
}

// Pool is a certificate pool that trusts only this authority.
func (a *Authority) Pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(a.certificate)
	return pool
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	if err != nil {
		panic(err)
	}
	return n
}
