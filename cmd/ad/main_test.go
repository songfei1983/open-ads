package main

import (
	"encoding/json"
	"os"
	"os/signal"
	"testing"
	"time"

	"github.com/confluentinc/confluent-kafka-go/kafka"
	"github.com/google/uuid"
	"github.com/songfei1983/open-ads/internal/model"
	"github.com/stretchr/testify/assert"
)

func TestMainKafkaProducer(t *testing.T) {
	// Set test environment variables, ensure errors are handled
	if err := os.Setenv("KAFKA_SERVER", "localhost:9092"); err != nil {
		t.Fatalf("failed to set KAFKA_SERVER: %v", err)
	}
	if err := os.Setenv("KAFKA_TOPIC", "test-topic"); err != nil {
		t.Fatalf("failed to set KAFKA_TOPIC: %v", err)
	}
	// use t.Cleanup for teardown
	t.Cleanup(func() {
		_ = os.Unsetenv("KAFKA_SERVER")
		_ = os.Unsetenv("KAFKA_TOPIC")
	})

	// Create test producer
	p, err := kafka.NewProducer(&kafka.ConfigMap{
		"bootstrap.servers": os.Getenv("KAFKA_SERVER"),
		// Add test-specific configurations
		"message.timeout.ms": 1000,
	})
	assert.NoError(t, err)
	defer p.Close()

	// Test message delivery handler
	go func() {
		for e := range p.Events() {
			switch ev := e.(type) {
			case *kafka.Message:
				if ev.TopicPartition.Error != nil {
					t.Errorf("Delivery failed: %v\n", ev.TopicPartition.Error)
				}
			}
		}
	}()

	// Test order creation
	testOrder := model.Order{
		ID:        uuid.New().String(),
		ProductId: uuid.New().String(),
		UserId:    uuid.New().String(),
		Amount:    456000,
	}

	// Test order serialization
	orderJSON, err := json.Marshal(testOrder)
	assert.NoError(t, err)

	// Get KAFKA_TOPIC from environment variable
	topic := os.Getenv("KAFKA_TOPIC")
	// Test message production
	err = p.Produce(&kafka.Message{
		TopicPartition: kafka.TopicPartition{Topic: &topic, Partition: kafka.PartitionAny},
		Value:          orderJSON,
	}, nil)
	assert.NoError(t, err)

	// Wait for message delivery
	p.Flush(1000)
}

func TestMainOrderSerialization(t *testing.T) {
	order := model.Order{
		ID:        uuid.New().String(),
		ProductId: uuid.New().String(),
		UserId:    uuid.New().String(),
		Amount:    456000,
	}

	// Test serialization
	orderJSON, err := json.Marshal(order)
	assert.NoError(t, err)

	// Test deserialization
	var decodedOrder model.Order
	err = json.Unmarshal(orderJSON, &decodedOrder)
	assert.NoError(t, err)

	// Verify all fields match
	assert.Equal(t, order.ID, decodedOrder.ID)
	assert.Equal(t, order.ProductId, decodedOrder.ProductId)
	assert.Equal(t, order.UserId, decodedOrder.UserId)
	assert.Equal(t, order.Amount, decodedOrder.Amount)
}

// Fuzz test leveraging Go's built-in fuzzing (supported since 1.18, improved over time)
func FuzzOrderSerialization(f *testing.F) {
	// seed with a typical order
	f.Add("id", "prod", "user", 123)

	f.Fuzz(func(t *testing.T, id, pid, uid string, amount int) {
		order := model.Order{ID: id, ProductId: pid, UserId: uid, Amount: amount}
		data, err := json.Marshal(order)
		if err != nil {
			t.Skipf("marshal failed: %v", err)
		}
		var decoded model.Order
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}
		if decoded != order {
			t.Errorf("roundtrip mismatch: got %+v, want %+v", decoded, order)
		}
	})
}

func TestMainLoopCounter(t *testing.T) {
	count := 0
	maxCount := 10
	sigChan := make(chan os.Signal, 1)

	for i := 0; i < maxCount; i++ {
		select {
		case <-sigChan:
			return
		default:
			count++
		}
	}

	assert.Equal(t, maxCount, count, "Loop should execute exactly maxCount times")
}

func TestMainSignalHandling(t *testing.T) {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)

	// Test signal handling
	go func() {
		time.Sleep(100 * time.Millisecond)
		sigChan <- os.Interrupt
	}()

	// Verify signal handling
	select {
	case <-sigChan:
		// Test passed if we receive the signal
	case <-time.After(200 * time.Millisecond):
		t.Error("Signal handling timeout")
	}
}
