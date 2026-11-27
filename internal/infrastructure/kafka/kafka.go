// Package kafka tells whether Notifications reaches Kafka, for its readiness signal. Notifications
// consumes its event families through the shared service library's inbox consumer.
package kafka

import (
	"context"
	"fmt"

	"github.com/twmb/franz-go/pkg/kgo"
)

// Brokers are Kafka's brokers.
type Brokers struct {
	client *kgo.Client
}

// Open returns the brokers at addresses. It connects only when asked.
func Open(addresses []string) (*Brokers, error) {
	client, err := kgo.NewClient(kgo.SeedBrokers(addresses...))
	if err != nil {
		return nil, fmt.Errorf("kafka: %w", err)
	}
	return &Brokers{client: client}, nil
}

// Reachable reports whether any broker answers.
func (b *Brokers) Reachable(ctx context.Context) error {
	if err := b.client.Ping(ctx); err != nil {
		return fmt.Errorf("kafka: %w", err)
	}
	return nil
}

// Close closes the connections to the brokers.
func (b *Brokers) Close() {
	b.client.Close()
}
