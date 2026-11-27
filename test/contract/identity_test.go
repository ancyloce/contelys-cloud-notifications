//go:build contract

// Package contract holds Notifications' fast checks against the shared contracts (Notifications
// ADR 0002): each call that it makes, and each producer's events, against the contracts as they are
// in contracts/, which only this target reads. Ticket T18 adds the neighbours' previous released
// version and the latest product version (ADR 0057).
package contract

import (
	"os"
	"testing"

	"go.yaml.in/yaml/v3"
)

// contracts is the shared contracts' directory, mounted beside Notifications in the parent
// repository.
const contracts = "../../../../contracts/"

// Notifications' readiness signal of Identity reads the key set with no token, through Edge's
// internal listener.
func TestTheKeySetThatTheIdentitySignalReadsNeedsNoToken(t *testing.T) {
	var contract struct {
		Paths map[string]map[string]struct {
			OperationID string           `yaml:"operationId"`
			Security    []map[string]any `yaml:"security"`
		} `yaml:"paths"`
	}
	read(t, "openapi/identity/openapi.yaml", &contract)

	keys, ok := contract.Paths["/internal/v1/keys"]["get"]
	if !ok || keys.OperationID != "getKeys" {
		t.Fatalf("Identity's contract has no getKeys at GET /internal/v1/keys")
	}
	if len(keys.Security) != 0 {
		t.Errorf("getKeys asks for %v, want no token", keys.Security)
	}
}

// No deployable may call Notifications with a workload token, and it holds no protected writes.
func TestNotificationsAccessFileListsNoOperation(t *testing.T) {
	var access struct {
		ProtectedWrites *bool          `yaml:"protected_writes"`
		Operations      map[string]any `yaml:"operations"`
	}
	read(t, "access/notifications.yaml", &access)

	if access.ProtectedWrites == nil || *access.ProtectedWrites {
		t.Errorf("protected_writes is %v, want false", access.ProtectedWrites)
	}
	if len(access.Operations) != 0 {
		t.Errorf("operations are %v, want none", access.Operations)
	}
}

func read(t *testing.T, path string, into any) {
	t.Helper()
	content, err := os.ReadFile(contracts + path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(content, into); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}
