// Package retention holds how long Notifications keeps what it records (spec, "Data").
package retention

import (
	"fmt"
	"time"
)

// LongestSubscribedTopic is the longest time for which a topic that Notifications subscribes to
// keeps its events: 15 days, for identity.principal and organization-access.organization
// (ADR 0029).
const LongestSubscribedTopic = 15 * 24 * time.Hour

// MinimumIdentities is the shortest retention of a notification's identity: one day longer than
// the longest-kept subscribed topic, so that no replay can send the same email again.
const MinimumIdentities = LongestSubscribedTopic + 24*time.Hour

// CheckIdentities refuses a retention of identities below MinimumIdentities.
func CheckIdentities(d time.Duration) error {
	if d < MinimumIdentities {
		return fmt.Errorf("a retention of identities of %s is below the minimum of %s", d, MinimumIdentities)
	}
	return nil
}
