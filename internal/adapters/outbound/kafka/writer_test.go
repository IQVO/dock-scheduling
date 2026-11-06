package kafka

import (
	"testing"

	kafkago "github.com/segmentio/kafka-go"
)

func TestNewWriterSettings(t *testing.T) {
	w := NewWriter(" a:9092, b:9092,")
	if w.RequiredAcks != kafkago.RequireAll {
		t.Fatalf("acks = %v, want RequireAll", w.RequiredAcks)
	}
	if _, ok := w.Balancer.(*kafkago.Hash); !ok {
		t.Fatalf("balancer = %T, want *kafka.Hash", w.Balancer)
	}
	if w.Topic != "" {
		t.Fatalf("writer must not fix a topic, got %q", w.Topic)
	}
	if !w.AllowAutoTopicCreation {
		t.Fatal("AllowAutoTopicCreation must be on")
	}
	if got := w.Addr.String(); got != "a:9092,b:9092" {
		t.Fatalf("addr = %q", got)
	}
}

func TestSplitBrokersDropsBlanks(t *testing.T) {
	if got := splitBrokers(" , "); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}
