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
	kafkaServer := os.Getenv("TEST_KAFKA_SERVER")
	if kafkaServer == "" {
		t.Skip("Skipping integration test: TEST_KAFKA_SERVER not set (requires a reachable Kafka broker). " +
			"Run with TEST_KAFKA_SERVER=host:9092 TEST_KAFKA_TOPIC=test go test -v ./cmd/ad to enable.")
	}
	kafkaTopic := os.Getenv("TEST_KAFKA_TOPIC")
	if kafkaTopic == "" {
		kafkaTopic = "test-topic"
	}

	t.Setenv("KAFKA_SERVER", kafkaServer)
	t.Setenv("KAFKA_TOPIC", kafkaTopic)

	p, err := kafka.NewProducer(&kafka.ConfigMap{
		"bootstrap.servers":        kafkaServer,
		"message.timeout.ms":       3000,
		"socket.timeout.ms":        5000,
		"socket.connection.setup.timeout.ms": 2000,
		"api.version.request.timeout.ms": 2000,
		"metadata.request.timeout.ms": 2000,
	})
	if err != nil {
		t.Fatalf("failed to create producer: %v", err)
	}
	defer p.Close()

	deliveryResults := make(chan error, 1)
	go func() {
		defer close(deliveryResults)
		for e := range p.Events() {
			switch ev := e.(type) {
			case *kafka.Message:
				if ev.TopicPartition.Error != nil {
					deliveryResults <- ev.TopicPartition.Error
					return
				}
				deliveryResults <- nil
				return
			}
		}
	}()

	testOrder := model.Order{
		ID:        uuid.New().String(),
		ProductId: uuid.New().String(),
		UserId:    uuid.New().String(),
		Amount:    456000,
	}

	orderJSON, err := json.Marshal(testOrder)
	assert.NoError(t, err)

	topic := kafkaTopic
	err = p.Produce(&kafka.Message{
		TopicPartition: kafka.TopicPartition{Topic: &topic, Partition: kafka.PartitionAny},
		Value:          orderJSON,
	}, nil)
	assert.NoError(t, err)

	flushed := p.Flush(5000)
	assert.Equal(t, 0, flushed, "expected all messages to be flushed (0 remaining)")

	select {
	case err := <-deliveryResults:
		assert.NoError(t, err, "Kafka message delivery should succeed")
	case <-time.After(6 * time.Second):
		t.Fatal("timed out waiting for Kafka delivery report")
	}
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
