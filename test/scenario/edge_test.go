//go:build scenario

package scenario

import (
	"crypto/tls"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ancyloce/contelys-cloud-notifications/test/scenario/identity"
)

// internalListener stands for Edge's internal listener, through which Notifications makes every
// call: a TLS server in this process, with a certificate from the run's authority, that serves
// each fake neighbour under its slug, as /identity/internal/v1/keys.
type internalListener struct {
	server   *http.Server
	port     int
	identity *identity.Fake
	stopOnce sync.Once
}

// startInternalListener serves a fake Identity until the scenario ends.
func startInternalListener(t *testing.T) *internalListener {
	t.Helper()
	certificate, err := env.ca.Issue(hostGateway)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "0.0.0.0:0") // the container connects through the host gateway
	if err != nil {
		t.Fatal(err)
	}
	l := &internalListener{port: listener.Addr().(*net.TCPAddr).Port, identity: identity.New()}
	mux := http.NewServeMux()
	mux.Handle("/identity/", http.StripPrefix("/identity", l.identity.Handler()))
	l.server = &http.Server{
		Handler:           mux,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{certificate.Pair}},
		ReadHeaderTimeout: 10 * time.Second,
	}
	go l.server.ServeTLS(listener, "", "")
	t.Cleanup(l.stop)
	return l
}

// url is where the container reaches the listener.
func (l *internalListener) url() string {
	return "https://" + hostGateway + ":" + strconv.Itoa(l.port)
}

// stop closes the listener, so that the neighbours behind it cannot be reached.
func (l *internalListener) stop() {
	l.stopOnce.Do(func() { l.server.Close() })
}
