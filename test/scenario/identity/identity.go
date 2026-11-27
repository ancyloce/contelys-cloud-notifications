// Package identity is the scenarios' fake of Identity, on the server interface generated from
// Identity's contract into gen/ (Notifications ADR 0002). It issues workload tokens, answers the
// principal lookup from the principals a scenario gives it, serves an empty key set, and records
// each request.
package identity

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/ancyloce/contelys-cloud-notifications/test/scenario/identity/gen"
)

// Request is one request that the fake received.
type Request struct {
	Method, Path string
	// Caller is the user name of HTTP Basic, which a workload-token request carries.
	Caller string
}

// Fake is a fake Identity.
type Fake struct {
	mu         sync.Mutex
	requests   []Request
	principals map[string]gen.FoundPrincipal
}

// New returns a fake that knows no principal.
func New() *Fake {
	return &Fake{principals: map[string]gen.FoundPrincipal{}}
}

// Handler serves the fake's operations at the paths of Identity's contract.
func (f *Fake) Handler() http.Handler {
	return gen.Handler(f)
}

// Know makes the fake answer a lookup of the principal's identifier with it.
func (f *Fake) Know(principal gen.FoundPrincipal) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.principals[principal.Id] = principal
}

// Requests returns the requests received so far.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Request(nil), f.requests...)
}

func (f *Fake) record(r *http.Request) {
	caller, _, _ := r.BasicAuth()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, Request{Method: r.Method, Path: r.URL.Path, Caller: caller})
}

// GetKeys serves an empty key set: Notifications verifies no token.
func (f *Fake) GetKeys(w http.ResponseWriter, r *http.Request) {
	f.record(r)
	answer(w, http.StatusOK, gen.JsonWebKeySet{Keys: []gen.Ed25519Key{}})
}

// IssueWorkloadToken issues an opaque token for the target, which lives five minutes.
func (f *Fake) IssueWorkloadToken(w http.ResponseWriter, r *http.Request) {
	f.record(r)
	var request gen.IssueWorkloadTokenJSONRequestBody
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Target == "" {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	caller, _, _ := r.BasicAuth()
	answer(w, http.StatusOK, gen.WorkloadToken{
		Token:     "workload-token:" + caller + ":" + request.Target,
		ExpiresAt: time.Now().Add(5 * time.Minute).UTC(),
	})
}

// LookUpPrincipals answers each known principal, and lists the others as unknown.
func (f *Fake) LookUpPrincipals(w http.ResponseWriter, r *http.Request) {
	f.record(r)
	var request gen.LookUpPrincipalsJSONRequestBody
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Principals) == 0 {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	result := gen.PrincipalLookupResult{Principals: []gen.FoundPrincipal{}, Unknown: []gen.PrincipalId{}}
	f.mu.Lock()
	for _, id := range request.Principals {
		if principal, ok := f.principals[id]; ok {
			result.Principals = append(result.Principals, principal)
		} else {
			result.Unknown = append(result.Unknown, id)
		}
	}
	f.mu.Unlock()
	answer(w, http.StatusOK, result)
}

func answer(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
