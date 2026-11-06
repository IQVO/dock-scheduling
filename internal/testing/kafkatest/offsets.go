package kafkatest

import (
	"context"
	"testing"
	"time"

	kafkago "github.com/segmentio/kafka-go"
)

// TryCommittedOffset is CommittedOffset for a poll loop: it never fails the
// test. ok is false while the group has no committed offset on partition 0 of
// topic yet (the group has not joined, or the coordinator has not answered).
func TryCommittedOffset(t *testing.T, group, topic string) (offset int64, ok bool) {
	t.Helper()
	client := &kafkago.Client{Addr: kafkago.TCP(Brokers(t)...), Timeout: 10 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	resp, err := client.OffsetFetch(ctx, &kafkago.OffsetFetchRequest{GroupID: group, Topics: map[string][]int{topic: {0}}})
	if err != nil {
		return -1, false
	}
	for _, p := range resp.Topics[topic] {
		if p.Partition == 0 && p.Error == nil && p.CommittedOffset >= 0 {
			return p.CommittedOffset, true
		}
	}
	return -1, false
}
