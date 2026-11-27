//go:build scenario

package scenario

import (
	"testing"
)

// neighbours are the readiness signals of what Notifications reaches over the network. None of them
// takes an instance out of routing, because the work waits safely for them (spec, "Deployment and
// operations").
var neighbours = []string{"kafka", "identity", "mail"}

// Kafka, Identity behind Edge's internal listener, and the mail server are reported separately.
func TestTheNeighboursAreSeparateSignals(t *testing.T) {
	edge := startInternalListener(t)
	n := start(t, defaults(t).set("internal.url", edge.url()))

	answer := n.eventually(t, "the neighbours' signals never passed", func(r readiness) bool {
		return r.Ready && passed(t, r, neighbours...)
	})

	for _, name := range neighbours {
		if _, required := answer.check(t, name); required {
			t.Errorf("check %s is required, want a signal that leaves the instance ready", name)
		}
	}
	if len(edge.identity.Requests()) == 0 {
		t.Errorf("the fake Identity received no request")
	}
}

// An instance stays ready while a neighbour cannot be reached, and only that neighbour's signal
// fails.
func TestAnInstanceStaysReadyWhileANeighbourCannotBeReached(t *testing.T) {
	for _, c := range []struct {
		neighbour string
		cut       func(s settings, edge *internalListener) settings
	}{
		{"kafka", func(s settings, _ *internalListener) settings {
			return s.set("kafka.brokers", []string{"absent:9092"})
		}},
		{"identity", func(s settings, edge *internalListener) settings {
			edge.stop()
			return s
		}},
		{"mail", func(s settings, _ *internalListener) settings { return s.set("smtp.host", "absent") }},
	} {
		t.Run(c.neighbour, func(t *testing.T) {
			t.Parallel()
			edge := startInternalListener(t)
			n := start(t, c.cut(defaults(t).set("internal.url", edge.url()), edge))

			answer := n.eventually(t, "the other signals never passed", func(r readiness) bool {
				return r.Ready && passed(t, r, others(c.neighbour)...)
			})

			if ok, _ := answer.check(t, c.neighbour); ok {
				t.Errorf("the %s signal passed although it cannot be reached", c.neighbour)
			}
		})
	}
}

// A neighbour that goes away while the instance runs fails its signal, and the instance stays ready.
func TestASignalFollowsANeighbourThatGoesAway(t *testing.T) {
	edge := startInternalListener(t)
	n := start(t, defaults(t).set("internal.url", edge.url()))
	n.eventually(t, "the identity signal never passed", func(r readiness) bool { return passed(t, r, "identity") })

	edge.stop()

	n.eventually(t, "the identity signal kept passing", func(r readiness) bool {
		ok, _ := r.check(t, "identity")
		return r.Ready && !ok
	})
}

// The mail server's certificate is verified against smtp.ca: without it, a server signed by the
// installation's own authority cannot be reached.
func TestTheMailServerIsVerifiedAgainstItsAuthority(t *testing.T) {
	edge := startInternalListener(t)
	n := start(t, defaults(t).set("internal.url", edge.url()).set("smtp.ca", ""))

	answer := n.eventually(t, "the other signals never passed", func(r readiness) bool {
		return r.Ready && passed(t, r, "kafka", "identity")
	})

	if ok, _ := answer.check(t, "mail"); ok {
		t.Errorf("the mail signal passed without the mail server's authority")
	}
}

// Plain SMTP is possible only when smtp.tls says none, and is then reported as a warning, by a
// signal that leaves the instance ready and by a line of its log (story 30).
func TestPlainSMTPIsReportedAsAWarning(t *testing.T) {
	edge := startInternalListener(t)
	plain := start(t, defaults(t).set("internal.url", edge.url()).set("smtp.tls", "none"))
	encrypted := start(t, defaults(t).set("internal.url", edge.url()))

	answer := plain.ready(t)
	if ok, required := answer.check(t, "mail-tls"); ok || required {
		t.Errorf("mail-tls with smtp.tls none: ok %v, required %v; want a failing signal", ok, required)
	}
	plain.waitForLog(t, `"level":"WARN"`, "smtp.tls")
	if ok, _ := encrypted.ready(t).check(t, "mail-tls"); !ok {
		t.Errorf("mail-tls fails with smtp.tls starttls")
	}
}

// passed reports whether every named check of the answer passed.
func passed(t *testing.T, r readiness, names ...string) bool {
	t.Helper()
	for _, name := range names {
		if ok, _ := r.check(t, name); !ok {
			return false
		}
	}
	return true
}

// others are the neighbours' signals except one.
func others(except string) []string {
	var out []string
	for _, name := range neighbours {
		if name != except {
			out = append(out, name)
		}
	}
	return out
}
