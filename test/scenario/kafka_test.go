//go:build scenario

package scenario

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"
	"google.golang.org/protobuf/proto"
)

// event is one record as a producer's relay publishes it: the Protobuf message alone as its value,
// and the CloudEvents headers of ADR 0061 with the W3C trace context. A replay through Event Center
// is the same event published again, with the same ce_id.
type event struct {
	topic, key string
	message    proto.Message
	// id, source and occurred are ce_id, ce_source and ce_time. An empty id is a new UUID, and a
	// zero time is now.
	id, source string
	occurred   time.Time
	// headers replace the CloudEvents headers by name, and an empty value removes one, for the
	// records that a scenario sends to the dead letters.
	headers map[string]string
}

// record is the Kafka record of the event.
func (e event) record(t *testing.T) *kgo.Record {
	t.Helper()
	value, err := proto.Marshal(e.message)
	if err != nil {
		t.Fatal(err)
	}
	if e.id == "" {
		e.id = uuid.NewString()
	}
	if e.occurred.IsZero() {
		e.occurred = time.Now()
	}
	values := map[string]string{
		"ce_specversion": "1.0",
		"ce_id":          e.id,
		"ce_source":      e.source,
		"ce_type":        string(proto.MessageName(e.message)),
		"ce_time":        e.occurred.UTC().Format(time.RFC3339Nano),
		"content-type":   "application/protobuf",
		"traceparent":    newTraceparent(t),
	}
	order := []string{"ce_specversion", "ce_id", "ce_source", "ce_type", "ce_time", "content-type", "traceparent"}
	for name, value := range e.headers {
		if _, known := values[name]; !known {
			order = append(order, name)
		}
		values[name] = value
	}
	record := &kgo.Record{Topic: e.topic, Key: []byte(e.key), Value: value}
	for _, name := range order {
		if values[name] != "" {
			record.Headers = append(record.Headers, kgo.RecordHeader{Key: name, Value: []byte(values[name])})
		}
	}
	return record
}

// newTraceparent is the W3C trace context of a new, sampled trace, as a producer's span gives it.
func newTraceparent(t *testing.T) string {
	t.Helper()
	ids := make([]byte, 24)
	if _, err := rand.Read(ids); err != nil {
		t.Fatal(err)
	}
	return "00-" + hex.EncodeToString(ids[:16]) + "-" + hex.EncodeToString(ids[16:]) + "-01"
}

// publish publishes the events, in order, and waits until Kafka has them.
func publish(t *testing.T, events ...event) {
	t.Helper()
	client := kafkaClient(t)
	var records []*kgo.Record
	for _, e := range events {
		records = append(records, e.record(t))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := client.ProduceSync(ctx, records...).FirstErr(); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

// createTopic creates a topic, as Event Center does for a family, unless it exists.
func createTopic(t *testing.T, name string, partitions int32) {
	t.Helper()
	request := kmsg.NewPtrCreateTopicsRequest()
	topic := kmsg.NewCreateTopicsRequestTopic()
	topic.Topic, topic.NumPartitions, topic.ReplicationFactor = name, partitions, 1
	request.Topics = append(request.Topics, topic)
	response, err := request.RequestWith(context.Background(), kafkaClient(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, created := range response.Topics {
		if created.ErrorCode != 0 && created.ErrorCode != 36 { // 36: the topic already exists
			t.Fatalf("create topic %s: error code %d", created.Topic, created.ErrorCode)
		}
	}
}

// kafkaClient is a client of the run's Kafka, through its listener for this process.
func kafkaClient(t *testing.T) *kgo.Client {
	t.Helper()
	client, err := kgo.NewClient(kgo.SeedBrokers(env.kafkaBrokers))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.Close)
	return client
}
