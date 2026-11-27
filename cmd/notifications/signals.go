package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

const (
	// probeEvery is how often an instance probes each neighbour for its readiness signal, and
	// probeTimeout how long one probe may take.
	probeEvery   = 5 * time.Second
	probeTimeout = 10 * time.Second
)

// errNotProbed is a signal's state before its first probe has answered.
var errNotProbed = errors.New("not probed yet")

// watch probes a neighbour every probeEvery until ctx ends, and returns the check of a readiness
// signal that answers with the outcome of the last probe. Probing apart from the readiness requests
// keeps a slow neighbour from delaying them, and keeps a mail provider from being dialled by every
// prober of readiness.
func watch(ctx context.Context, probe func(context.Context) error) func(context.Context) error {
	var mu sync.Mutex
	last := errNotProbed
	go func() {
		for {
			probing, cancel := context.WithTimeout(ctx, probeTimeout)
			err := probe(probing)
			cancel()
			mu.Lock()
			last = err
			mu.Unlock()
			select {
			case <-ctx.Done():
				return
			case <-time.After(probeEvery):
			}
		}
	}()
	return func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}
