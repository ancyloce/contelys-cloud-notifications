//go:build scenario

package scenario

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go/wait"
	"go.yaml.in/yaml/v3"
)

// A configuration that Notifications refuses is never applied (spec, "Configuration"). At start,
// the instance exits with the reason in its log and never reports ready.
func TestAnInvalidConfigurationIsRefusedAtStart(t *testing.T) {
	for _, c := range []struct {
		name, path string
		value      any
		reason     string
	}{
		// One day longer than the 15 days of the longest-kept subscribed topic (ADR 0029).
		{"a retention of identities below 16 days", "retention.identities", "383h", "retention.identities"},
		{"an unknown TLS mode", "smtp.tls", "ssl", "smtp.tls"},
		{"no TLS mode", "smtp.tls", "", "smtp.tls"},
		{"a database URL with a password", "database.url", "postgres://notifications:secret@postgres:5432/notifications", "database.url"},
		{"links outside https", "links.base", "http://app.example.test", "links.base"},
		// Switching email off is a choice, never the default.
		{"no email.enabled", "email.enabled", nil, "email.enabled"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			code, log := refused(t, defaults(t).set(c.path, c.value))
			if code == 0 {
				t.Fatalf("exit code 0, want a refusal")
			}
			if !strings.Contains(log, c.reason) {
				t.Errorf("the log does not name %s:\n%s", c.reason, log)
			}
			if strings.Contains(log, "secret") {
				t.Errorf("the log carries the password of the refused URL:\n%s", log)
			}
		})
	}
}

// The retention periods change without a restart. A changed file that would lower the retention
// of identities below its minimum is refused, and the configuration in force stays.
func TestALiveChangeBelowTheMinimumRetentionIsRefused(t *testing.T) {
	s := defaults(t)
	n := start(t, s)
	n.ready(t)

	s["revision"] = "fedcba9876543210fedcba9876543210fedcba98"
	n.write(t, configDir+"/config.yaml", s.set("retention.identities", "360h").yaml(t))

	n.waitForLog(t, "a changed configuration was refused", "retention.identities")
	if got := n.revision(t); got != revision {
		t.Errorf("the revision in force is %s, want %s", got, revision)
	}
	n.ready(t)
	// Docker's health check agrees with readiness: the refused file is not in force.
	if code := n.health(t); code != 0 {
		t.Errorf("health exited %d after a refused live change, want 0 as readiness answers", code)
	}
}

// refused starts an instance that is expected to exit, and returns its exit code and its log.
func refused(t *testing.T, s settings) (int, string) {
	t.Helper()
	n := run(t, s, wait.ForExit().WithExitTimeout(30*time.Second))
	state, err := n.container.State(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return state.ExitCode, logsOf(n.container)
}

// yaml renders the settings as the configuration file.
func (s settings) yaml(t *testing.T) []byte {
	t.Helper()
	out, err := yaml.Marshal(map[string]any(s))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// write replaces a file that the instance reads.
func (n *instance) write(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := n.container.CopyToContainer(context.Background(), content, path, 0o644); err != nil {
		t.Fatal(err)
	}
}

// waitForLog waits until a line of the instance's log carries every one of parts.
func (n *instance) waitForLog(t *testing.T, parts ...string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		for _, line := range strings.Split(logsOf(n.container), "\n") {
			if containsAll(line, parts) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no log line carries %q:\n%s", parts, logsOf(n.container))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func containsAll(s string, parts []string) bool {
	for _, part := range parts {
		if !strings.Contains(s, part) {
			return false
		}
	}
	return true
}

// revision is the revision of the configuration in force, as GET /revision answers it.
func (n *instance) revision(t *testing.T) string {
	t.Helper()
	endpoint, err := n.container.PortEndpoint(context.Background(), listener, "https")
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: clientTLS()}}
	response, err := client.Get(endpoint + "/revision")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var answer struct {
		Revision string `json:"revision"`
	}
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		t.Fatal(err)
	}
	return answer.Revision
}
