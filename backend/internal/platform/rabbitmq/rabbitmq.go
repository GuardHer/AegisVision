package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"sync"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Client wraps an AMQP connection and thread-safe publisher channel.
type Client struct {
	conn  *amqp.Connection
	pubMu sync.Mutex
	pubCh *amqp.Channel
}

// New connects to RabbitMQ at amqpURL and initializes a publisher channel.
func New(amqpURL string) (*Client, error) {
	conn, err := amqp.Dial(amqpURL)
	if err != nil {
		return nil, fmt.Errorf("dial rabbitmq: %w", err)
	}
	pubCh, err := conn.Channel()
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("open rabbitmq publish channel: %w", err)
	}
	return &Client{
		conn:  conn,
		pubCh: pubCh,
	}, nil
}

// Close shuts down the publisher channel and AMQP connection.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}
	c.pubMu.Lock()
	var errs []error
	if c.pubCh != nil {
		if err := c.pubCh.Close(); err != nil && !errors.Is(err, amqp.ErrClosed) {
			errs = append(errs, err)
		}
		c.pubCh = nil
	}
	c.pubMu.Unlock()

	if c.conn != nil {
		if err := c.conn.Close(); err != nil && !errors.Is(err, amqp.ErrClosed) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (c *Client) publish(ctx context.Context, exchange, routingKey string, persistent bool, body []byte) error {
	mode := amqp.Transient
	if persistent {
		mode = amqp.Persistent
	}

	c.pubMu.Lock()
	defer c.pubMu.Unlock()
	if c.pubCh == nil {
		return errors.New("rabbitmq publish channel is closed")
	}

	return c.pubCh.PublishWithContext(ctx, exchange, routingKey, false, false, amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: mode,
		Body:         body,
	})
}

// PublishAlarm enqueues a newly triggered alarm onto the durable alarms.triggered queue.
func (c *Client) PublishAlarm(ctx context.Context, body []byte) error {
	return c.publish(ctx, ExchangeAlarms, QueueAlarmsTriggered, true, body)
}

// PublishAlarmRetry routes a failed delivery to the appropriate DLX backoff queue (3s -> 15s -> 60s -> dlq).
func (c *Client) PublishAlarmRetry(ctx context.Context, failedAttempts int, body []byte) error {
	var routingKey string
	switch failedAttempts {
	case 1:
		routingKey = QueueAlarmsRetry3s
	case 2:
		routingKey = QueueAlarmsRetry15s
	case 3:
		routingKey = QueueAlarmsRetry60s
	default:
		routingKey = QueueAlarmsDLQ
	}
	return c.publish(ctx, ExchangeAlarms, routingKey, true, body)
}

// PublishEvidenceRecord enqueues an asynchronous MP4 video evidence recording request.
func (c *Client) PublishEvidenceRecord(ctx context.Context, body []byte) error {
	return c.publish(ctx, ExchangeEvidence, QueueEvidenceRecord, true, body)
}

// PublishAgentCommand routes a scheduler command to the ingest instance holding the target Agent stream.
func (c *Client) PublishAgentCommand(ctx context.Context, ingestInstanceID string, body []byte) error {
	return c.publish(ctx, ExchangeControl, AgentCommandRoutingKey(ingestInstanceID), false, body)
}

// PublishConfigEvent broadcasts a configuration change event to all subscribers.
func (c *Client) PublishConfigEvent(ctx context.Context, body []byte) error {
	return c.publish(ctx, ExchangeConfig, "", false, body)
}

// DeclareExclusiveBinding creates a temporary auto-delete queue bound to exchange/routingKey
// (used for per-instance agent command routing and config fanout subscriptions).
func (c *Client) DeclareExclusiveBinding(ctx context.Context, exchange, routingKey string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	ch, err := c.conn.Channel()
	if err != nil {
		return "", fmt.Errorf("open exclusive binding channel: %w", err)
	}
	defer func() { _ = ch.Close() }()

	// Auto-delete non-exclusive queue so Consume can open its own dedicated channel against it.
	q, err := ch.QueueDeclare("", false, true, false, false, nil)
	if err != nil {
		return "", fmt.Errorf("declare temp queue: %w", err)
	}
	if err := ch.QueueBind(q.Name, routingKey, exchange, false, nil); err != nil {
		return "", fmt.Errorf("bind temp queue %q to %q (%q): %w", q.Name, exchange, routingKey, err)
	}
	return q.Name, nil
}

// Consume reads messages from queue with manual ACK/NACK until ctx is canceled or the channel closes.
func (c *Client) Consume(ctx context.Context, queue string, prefetch int, handler func(ctx context.Context, body []byte) error) error {
	ch, err := c.conn.Channel()
	if err != nil {
		return fmt.Errorf("open consumer channel: %w", err)
	}
	defer func() { _ = ch.Close() }()

	if prefetch > 0 {
		if err := ch.Qos(prefetch, 0, false); err != nil {
			return fmt.Errorf("set consumer qos: %w", err)
		}
	}

	deliveries, err := ch.Consume(queue, "", false, false, false, false, nil)
	if err != nil {
		return fmt.Errorf("consume queue %q: %w", queue, err)
	}

	workers := prefetch
	if workers <= 0 {
		workers = 1
	}
	sem := make(chan struct{}, workers)
	var wg sync.WaitGroup
	defer wg.Wait()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case d, ok := <-deliveries:
			if !ok {
				return errors.New("rabbitmq delivery channel closed")
			}
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return ctx.Err()
			}
			wg.Add(1)
			go func(del amqp.Delivery) {
				defer func() {
					<-sem
					wg.Done()
				}()
				if err := handler(ctx, del.Body); err != nil {
					_ = del.Nack(false, false)
					return
				}
				_ = del.Ack(false)
			}(d)
		}
	}
}
