package main

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	hashchain "snippet/hashchain"
)

// Topics the dispatch service writes (see the sheet): every event goes to the verified-events topic keyed by aggregate, and to the
// immutable audit topic keyed by sequence number.
const (
	EventsTopic = "incidents.verified.events"
	AuditTopic  = "audit.security.immutable"
)

// Publisher sends one record; it must return an error if the record was not accepted.
type Publisher interface {
	Publish(ctx context.Context, topic, key string, value []byte) error
}

// KafkaPublisher publishes to Redpanda/Kafka with franz-go, waiting for all in-sync replicas.
type KafkaPublisher struct{ cl *kgo.Client }

func NewKafkaPublisher(brokers []string) (*KafkaPublisher, error) {
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.RequiredAcks(kgo.AllISRAcks()))
	if err != nil {
		return nil, err
	}
	return &KafkaPublisher{cl: cl}, nil
}

func (k *KafkaPublisher) Publish(ctx context.Context, topic, key string, value []byte) error {
	return k.cl.ProduceSync(ctx, &kgo.Record{Topic: topic, Key: []byte(key), Value: value}).FirstErr()
}

func (k *KafkaPublisher) Close() { k.cl.Close() }

// discardPublisher is used when no broker is configured: events stay in the log and are not published.
type discardPublisher struct{ once sync.Once }

func (d *discardPublisher) Publish(context.Context, string, string, []byte) error {
	d.once.Do(func() { log.Printf("KAFKA_BROKERS is not set: events are stored but not published") })
	return nil
}

// Cursors remember, per tenant, the last sequence number that was published. With Postgres (PGCursors) it survives a restart; the in-memory one
// forgets, which only means events are published again (at-least-once): consumers dedupe on tenant_id + sequence_id either way, because a crash
// between publishing an event and saving the cursor still republishes that event.
type Cursors interface {
	Get(ctx context.Context, tenant string) (int64, error)
	Set(ctx context.Context, tenant string, seq int64) error
}

type memCursors struct {
	mu sync.Mutex
	m  map[string]int64
}

func NewMemCursors() Cursors { return &memCursors{m: map[string]int64{}} }

func (c *memCursors) Get(_ context.Context, tenant string) (int64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.m[tenant], nil
}

func (c *memCursors) Set(_ context.Context, tenant string, seq int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if seq > c.m[tenant] {
		c.m[tenant] = seq
	}
	return nil
}

// Outbox publishes the event log in order. The log itself is the queue: nothing is lost if the broker is down, because a command only
// needs the append to succeed, and the cursor moves only after both topics accepted an event, so a failure retries from the same event.
type Outbox struct {
	store   EventStore
	pub     Publisher
	cursors Cursors
	mu      sync.Mutex
	dirty   map[string]bool
	wake    chan struct{}
}

func NewOutbox(store EventStore, pub Publisher, cursors Cursors) *Outbox {
	return &Outbox{store: store, pub: pub, cursors: cursors, dirty: map[string]bool{}, wake: make(chan struct{}, 1)}
}

// outboxEvent is the Redpanda message value: the event record plus its tenant.
type outboxEvent struct {
	TenantID string `json:"tenant_id"`
	hashchain.Entry
}

// Notify says a tenant has new events; Run will publish them.
func (o *Outbox) Notify(tenant string) {
	o.mu.Lock()
	o.dirty[tenant] = true
	o.mu.Unlock()
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// Drain publishes everything after the tenant's cursor, in order, and stops at the first failure.
func (o *Outbox) Drain(ctx context.Context, tenant string) (int, error) {
	n := 0
	cur, err := o.cursors.Get(ctx, tenant)
	if err != nil {
		return 0, err
	}
	for {
		events, err := o.store.Load(ctx, tenant, cur, 200)
		if err != nil {
			return n, err
		}
		if len(events) == 0 {
			return n, nil
		}
		for _, e := range events {
			value, err := json.Marshal(outboxEvent{TenantID: tenant, Entry: e})
			if err != nil {
				return n, err
			}
			if err := o.pub.Publish(ctx, EventsTopic, e.AggregateID, value); err != nil {
				return n, err
			}
			if err := o.pub.Publish(ctx, AuditTopic, strconv.FormatInt(e.SequenceID, 10), value); err != nil {
				return n, err
			}
			if err := o.cursors.Set(ctx, tenant, e.SequenceID); err != nil {
				return n, err
			}
			cur = e.SequenceID
			n++
		}
	}
}

// Run publishes for notified tenants until ctx ends, retrying failed tenants every interval.
func (o *Outbox) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		o.mu.Lock()
		todo := make([]string, 0, len(o.dirty))
		for tn := range o.dirty {
			todo = append(todo, tn)
		}
		o.dirty = map[string]bool{}
		o.mu.Unlock()
		for _, tn := range todo {
			if _, err := o.Drain(ctx, tn); err != nil {
				log.Printf("outbox: tenant %s will retry: %v", tn, err)
				o.mu.Lock()
				o.dirty[tn] = true
				o.mu.Unlock()
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-o.wake:
		case <-t.C:
		}
	}
}
