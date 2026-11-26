package rabbitmq_test

import (
	"context"
	"os"
	"testing"
	"time"

	"aegisvision/backend/internal/platform/rabbitmq"
)

func TestTopologySpecContainsDLXRetryLadder(t *testing.T) {
	spec := rabbitmq.BuildTopologySpec()

	wantExchanges := map[string]string{
		rabbitmq.ExchangeAlarms:   "direct",
		rabbitmq.ExchangeEvidence: "direct",
		rabbitmq.ExchangeControl:  "topic",
		rabbitmq.ExchangeConfig:   "fanout",
	}
	for name, kind := range wantExchanges {
		got, ok := spec.Exchanges[name]
		if !ok || got != kind {
			t.Errorf("exchange %q = %q (exists=%v), want %q", name, got, ok, kind)
		}
	}

	wantRetryTTLs := map[string]int32{
		rabbitmq.QueueAlarmsRetry3s:  3000,
		rabbitmq.QueueAlarmsRetry15s: 15000,
		rabbitmq.QueueAlarmsRetry60s: 60000,
	}
	for qName, wantTTL := range wantRetryTTLs {
		q, ok := spec.Queues[qName]
		if !ok {
			t.Fatalf("missing retry queue %q in topology spec", qName)
		}
		if q.MessageTTLMs != wantTTL {
			t.Errorf("queue %q MessageTTLMs = %d, want %d", qName, q.MessageTTLMs, wantTTL)
		}
		if q.DeadLetterExchange != rabbitmq.ExchangeAlarms || q.DeadLetterRoutingKey != rabbitmq.QueueAlarmsTriggered {
			t.Errorf("queue %q DLX = (%q, %q), want (%q, %q)", qName, q.DeadLetterExchange, q.DeadLetterRoutingKey, rabbitmq.ExchangeAlarms, rabbitmq.QueueAlarmsTriggered)
		}
	}

	if _, ok := spec.Queues[rabbitmq.QueueAlarmsDLQ]; !ok {
		t.Errorf("missing %q in topology spec", rabbitmq.QueueAlarmsDLQ)
	}
	if _, ok := spec.Queues[rabbitmq.QueueEvidenceRecord]; !ok {
		t.Errorf("missing %q in topology spec", rabbitmq.QueueEvidenceRecord)
	}
}

func TestRabbitMQLiveTopologyAndAgentCommandRouting(t *testing.T) {
	amqpURL := os.Getenv("AEGIS_TEST_RABBITMQ_URL")
	if amqpURL == "" {
		t.Skip("AEGIS_TEST_RABBITMQ_URL not set; skipping live RabbitMQ test")
	}

	client, err := rabbitmq.New(amqpURL)
	if err != nil {
		t.Fatalf("rabbitmq.New() failed: %v", err)
	}
	defer func() { _ = client.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := client.EnsureTopology(ctx); err != nil {
		t.Fatalf("EnsureTopology() failed: %v", err)
	}

	// Verify targeted agent command routing on aegis.control topic exchange
	qName, err := client.DeclareExclusiveBinding(ctx, rabbitmq.ExchangeControl, rabbitmq.AgentCommandRoutingKey("ingest-test-1"))
	if err != nil {
		t.Fatalf("DeclareExclusiveBinding() failed: %v", err)
	}

	gotCh := make(chan []byte, 1)
	consumeCtx, consumeCancel := context.WithCancel(ctx)
	defer consumeCancel()

	go func() {
		_ = client.Consume(consumeCtx, qName, 1, func(_ context.Context, body []byte) error {
			gotCh <- append([]byte(nil), body...)
			consumeCancel()
			return nil
		})
	}()

	time.Sleep(150 * time.Millisecond)
	want := []byte(`{"command":"assign_task","task_id":"t-1"}`)
	if err := client.PublishAgentCommand(ctx, "ingest-test-1", want); err != nil {
		t.Fatalf("PublishAgentCommand() failed: %v", err)
	}

	select {
	case got := <-gotCh:
		if string(got) != string(want) {
			t.Fatalf("received command = %q, want %q", got, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for routed agent command")
	}
}
