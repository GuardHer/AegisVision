package rabbitmq

import (
	"context"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Exchanges defined by docs/design/02-protocols-and-apis.md section 6.1.
const (
	ExchangeAlarms   = "aegis.alarms"
	ExchangeEvidence = "aegis.evidence"
	ExchangeControl  = "aegis.control"
	ExchangeConfig   = "aegis.config"
)

// Durable queues and DLX retry ladder queues.
const (
	QueueAlarmsTriggered = "alarms.triggered"
	QueueAlarmsRetry3s   = "alarms.retry.3s"
	QueueAlarmsRetry15s  = "alarms.retry.15s"
	QueueAlarmsRetry60s  = "alarms.retry.60s"
	QueueAlarmsDLQ       = "alarms.dlq"
	QueueEvidenceRecord  = "evidence.record"
)

// AgentCommandRoutingKey builds the topic routing key for an ingest instance.
func AgentCommandRoutingKey(ingestInstanceID string) string {
	return "agent.cmd." + ingestInstanceID
}

// QueueSpec describes a durable queue and its optional DLX delay configuration.
type QueueSpec struct {
	Exchange             string
	RoutingKey           string
	MessageTTLMs         int32
	DeadLetterExchange   string
	DeadLetterRoutingKey string
}

// TopologySpec represents the complete broker exchange/queue declaration blueprint.
type TopologySpec struct {
	Exchanges map[string]string
	Queues    map[string]QueueSpec
}

// BuildTopologySpec returns the canonical AegisVision RabbitMQ topology.
func BuildTopologySpec() TopologySpec {
	return TopologySpec{
		Exchanges: map[string]string{
			ExchangeAlarms:   "direct",
			ExchangeEvidence: "direct",
			ExchangeControl:  "topic",
			ExchangeConfig:   "fanout",
		},
		Queues: map[string]QueueSpec{
			QueueAlarmsTriggered: {
				Exchange:   ExchangeAlarms,
				RoutingKey: QueueAlarmsTriggered,
			},
			QueueAlarmsRetry3s: {
				Exchange:             ExchangeAlarms,
				RoutingKey:           QueueAlarmsRetry3s,
				MessageTTLMs:         3000,
				DeadLetterExchange:   ExchangeAlarms,
				DeadLetterRoutingKey: QueueAlarmsTriggered,
			},
			QueueAlarmsRetry15s: {
				Exchange:             ExchangeAlarms,
				RoutingKey:           QueueAlarmsRetry15s,
				MessageTTLMs:         15000,
				DeadLetterExchange:   ExchangeAlarms,
				DeadLetterRoutingKey: QueueAlarmsTriggered,
			},
			QueueAlarmsRetry60s: {
				Exchange:             ExchangeAlarms,
				RoutingKey:           QueueAlarmsRetry60s,
				MessageTTLMs:         60000,
				DeadLetterExchange:   ExchangeAlarms,
				DeadLetterRoutingKey: QueueAlarmsTriggered,
			},
			QueueAlarmsDLQ: {
				Exchange:   ExchangeAlarms,
				RoutingKey: QueueAlarmsDLQ,
			},
			QueueEvidenceRecord: {
				Exchange:   ExchangeEvidence,
				RoutingKey: QueueEvidenceRecord,
			},
		},
	}
}

// EnsureTopology idempotently declares all AegisVision exchanges, queues, and bindings.
func (c *Client) EnsureTopology(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	ch, err := c.conn.Channel()
	if err != nil {
		return fmt.Errorf("open rabbitmq topology channel: %w", err)
	}
	defer func() { _ = ch.Close() }()

	spec := BuildTopologySpec()

	for name, kind := range spec.Exchanges {
		if err := ch.ExchangeDeclare(name, kind, true, false, false, false, nil); err != nil {
			return fmt.Errorf("declare exchange %q: %w", name, err)
		}
	}

	for qName, qSpec := range spec.Queues {
		var args amqp.Table
		if qSpec.MessageTTLMs > 0 || qSpec.DeadLetterExchange != "" {
			args = amqp.Table{}
			if qSpec.MessageTTLMs > 0 {
				args["x-message-ttl"] = qSpec.MessageTTLMs
			}
			if qSpec.DeadLetterExchange != "" {
				args["x-dead-letter-exchange"] = qSpec.DeadLetterExchange
			}
			if qSpec.DeadLetterRoutingKey != "" {
				args["x-dead-letter-routing-key"] = qSpec.DeadLetterRoutingKey
			}
		}

		if _, err := ch.QueueDeclare(qName, true, false, false, false, args); err != nil {
			return fmt.Errorf("declare queue %q: %w", qName, err)
		}
		if err := ch.QueueBind(qName, qSpec.RoutingKey, qSpec.Exchange, false, nil); err != nil {
			return fmt.Errorf("bind queue %q to %q (%q): %w", qName, qSpec.Exchange, qSpec.RoutingKey, err)
		}
	}

	return nil
}
