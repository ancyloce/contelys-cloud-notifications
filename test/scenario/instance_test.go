//go:build scenario

package scenario

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.yaml.in/yaml/v3"
)

const (
	// image is the image that the image target built.
	image = "contelys/notifications"
	// hostGateway is how the container reaches the fakes that run in this process.
	hostGateway = "host.docker.internal"
	// listener is the container port of Notifications' health endpoints, served over TLS with a
	// certificate for serverName.
	listener   = "8443/tcp"
	serverName = "notifications"
	// configDir is where the image reads its configuration, and secretsDir its secrets as files.
	configDir  = "/etc/notifications"
	secretsDir = configDir + "/secrets"
	revision   = "0123456789abcdef0123456789abcdef01234567"
)

// databases numbers the scenarios' databases.
var databases atomic.Int64

// newDatabase creates an empty database of the scenario's own and returns its name.
func newDatabase(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, env.admin)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	name := fmt.Sprintf("notifications_%d", databases.Add(1))
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	return name
}

// admin runs statement in the scenario's database as its owner.
func admin(t *testing.T, database, statement string) {
	t.Helper()
	ctx := context.Background()
	config, err := pgx.ParseConfig(env.admin)
	if err != nil {
		t.Fatal(err)
	}
	config.Database = database
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, statement); err != nil {
		t.Fatalf("%s: %v", statement, err)
	}
}

// database is the name of the scenario's database, which the defaults created.
func (s settings) database() string {
	url := s["database"].(map[string]any)["url"].(string)
	name := url[strings.LastIndex(url, "/")+1:]
	return name[:strings.Index(name, "?")]
}

// settings is the configuration file that Ansible would render for one instance, in the keys of
// the spec's table and those that reach the database, Kafka, Edge's internal listener and the
// instance's own TLS listener.
type settings map[string]any

// defaults is a valid configuration that reaches the run's PostgreSQL, Kafka and Mailpit, with a
// database of the scenario's own.
func defaults(t *testing.T) settings {
	t.Helper()
	return settings{
		"revision": revision,
		"listen":   ":8443",
		"tls": map[string]any{
			"certificate": configDir + "/tls.crt",
			"key":         secretsDir + "/tls.key",
			"authority":   configDir + "/ca.crt",
		},
		"database": map[string]any{
			"url":           "postgres://" + databaseUser + "@" + postgresHost + ":5432/" + newDatabase(t) + "?sslmode=disable",
			"password_file": secretsDir + "/database-password",
		},
		"kafka":    map[string]any{"brokers": []string{kafkaHost + ":9092"}},
		"internal": map[string]any{"url": "https://" + hostGateway + ":1"},
		"smtp": map[string]any{
			"host":     mailpitHost,
			"port":     1025,
			"tls":      "starttls",
			"ca":       configDir + "/smtp-ca.crt",
			"from":     "notifications@example.test",
			"timeouts": map[string]any{"connect": "5s", "command": "5s", "content": "30s"},
		},
		"email":     map[string]any{"enabled": true},
		"send":      map[string]any{"paused": false, "concurrency": 4, "attempts": 5, "backoff": map[string]any{"first": "1m", "longest": "1h"}},
		"links":     map[string]any{"base": "https://app.example.test"},
		"retention": map[string]any{"personalData": "720h", "identities": "384h"},
	}
}

// set replaces the setting at a dotted path, such as "smtp.tls".
func (s settings) set(path string, value any) settings {
	keys := strings.Split(path, ".")
	tree := map[string]any(s)
	for _, key := range keys[:len(keys)-1] {
		tree = tree[key].(map[string]any)
	}
	tree[keys[len(keys)-1]] = value
	return s
}

// instance is one running Notifications container.
type instance struct {
	container *testcontainers.DockerContainer
}

// files are what the instance reads: its configuration, the certificates and its secrets.
func files(t *testing.T, s settings, omit ...string) []testcontainers.ContainerFile {
	t.Helper()
	config, err := yaml.Marshal(map[string]any(s))
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := env.ca.Issue(serverName, "localhost")
	if err != nil {
		t.Fatal(err)
	}
	all := map[string][]byte{
		configDir + "/config.yaml":        config,
		configDir + "/tls.crt":            certificate.PEM,
		secretsDir + "/tls.key":           certificate.KeyPEM,
		configDir + "/ca.crt":             env.ca.PEM,
		configDir + "/smtp-ca.crt":        env.ca.PEM,
		secretsDir + "/database-password": []byte(databasePassword + "\n"),
	}
	var out []testcontainers.ContainerFile
	for path, content := range all {
		if !slices.Contains(omit, path) {
			out = append(out, testcontainers.ContainerFile{Reader: bytes.NewReader(content), ContainerFilePath: path, FileMode: 0o644})
		}
	}
	return out
}

// start starts an instance with the settings, and waits until it is live.
func start(t *testing.T, s settings, omit ...string) *instance {
	t.Helper()
	return run(t, s, wait.ForHTTP("/livez").WithPort(listener).WithTLS(true, clientTLS()).
		WithStartupTimeout(30*time.Second), omit...)
}

// run starts an instance with the settings and waits as ready says.
func run(t *testing.T, s settings, ready wait.Strategy, omit ...string) *instance {
	t.Helper()
	c, err := testcontainers.Run(context.Background(), image,
		network.WithNetwork(nil, env.network),
		testcontainers.WithExposedPorts(listener),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.ExtraHosts = append(hc.ExtraHosts, hostGateway+":host-gateway")
		}),
		testcontainers.WithFiles(files(t, s, omit...)...),
		testcontainers.WithWaitStrategy(ready),
	)
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatalf("start Notifications: %v\n%s", err, logsOf(c))
	}
	n := &instance{container: c}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("Notifications' log:\n%s", logsOf(c))
		}
	})
	return n
}

func logsOf(c *testcontainers.DockerContainer) string {
	if c == nil {
		return ""
	}
	r, err := c.Logs(context.Background())
	if err != nil {
		return err.Error()
	}
	defer r.Close()
	out, _ := io.ReadAll(r)
	return string(out)
}

// clientTLS verifies the instance's certificate against the run's authority.
func clientTLS() *tls.Config {
	return &tls.Config{RootCAs: env.ca.Pool(), ServerName: serverName}
}

// readiness is the body of GET /readyz, as the shared service library writes it.
type readiness struct {
	Ready  bool `json:"ready"`
	Checks []struct {
		Name     string `json:"name"`
		Required bool   `json:"required"`
		OK       bool   `json:"ok"`
	} `json:"checks"`
}

// check reports whether the named check passed, and fails the scenario if the answer lacks it.
func (r readiness) check(t *testing.T, name string) (ok, required bool) {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			return c.OK, c.Required
		}
	}
	t.Fatalf("the readiness answer has no check %q: %+v", name, r)
	return false, false
}

// readiness asks the instance once.
func (n *instance) readiness(t *testing.T) readiness {
	t.Helper()
	endpoint, err := n.container.PortEndpoint(context.Background(), listener, "https")
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: clientTLS()}}
	response, err := client.Get(endpoint + "/readyz")
	if err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	defer response.Body.Close()
	var answer readiness
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		t.Fatalf("GET /readyz: %v", err)
	}
	if (response.StatusCode == http.StatusOK) != answer.Ready {
		t.Fatalf("GET /readyz answered %d with ready %v", response.StatusCode, answer.Ready)
	}
	return answer
}

// eventually asks the instance until done holds for its readiness, for at most 30 seconds, and
// returns the last answer.
func (n *instance) eventually(t *testing.T, what string, done func(readiness) bool) readiness {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		answer := n.readiness(t)
		if done(answer) {
			return answer
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: the last readiness answer was %+v", what, answer)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// stays asks the instance until d has passed, and returns the first answer that is ready, or the
// last one, so that an instance that becomes ready for a moment is caught.
func (n *instance) stays(t *testing.T, d time.Duration) readiness {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		answer := n.readiness(t)
		if answer.Ready || time.Now().After(deadline) {
			return answer
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// ready waits until the instance reports ready.
func (n *instance) ready(t *testing.T) readiness {
	t.Helper()
	return n.eventually(t, "the instance never became ready", func(r readiness) bool { return r.Ready })
}
