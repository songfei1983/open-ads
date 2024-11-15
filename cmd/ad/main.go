package main

import (
	"encoding/json"
	"log"
	"os"
	"os/signal"

	"github.com/confluentinc/confluent-kafka-go/kafka"
	"github.com/google/uuid"
	"github.com/songfei1983/open-ads/internal/model"
)

func main() {
	// Create producer with configuration
	p, err := kafka.NewProducer(&kafka.ConfigMap{
		"bootstrap.servers": os.Getenv("KAFKA_SERVER"),
	})
	if err != nil {
		log.Fatalf("Failed to create producer: %v", err)
	}
	defer p.Close()

	// Handle delivery reports
	go func() {
		for e := range p.Events() {
			switch ev := e.(type) {
			case *kafka.Message:
				if ev.TopicPartition.Error != nil {
					log.Printf("Delivery failed: %v\n", ev.TopicPartition.Error)
				}
			}
		}
	}()

	topic := os.Getenv("KAFKA_TOPIC")
	order := model.Order{
		ID:        uuid.New().String(),
		ProductId: uuid.New().String(),
		UserId:    uuid.New().String(),
		Amount:    456000,
	}

	// Handle interrupt signal
	sigchan := make(chan os.Signal, 1)
	signal.Notify(sigchan, os.Interrupt)

	for i := 0; i < 100; i++ {
		select {
		case <-sigchan:
			return
		default:
			order.Amount = i * 100
			value, err := json.Marshal(order)
			if err != nil {
				log.Printf("Failed to marshal order: %v", err)
				continue
			}

			err = p.Produce(&kafka.Message{
				TopicPartition: kafka.TopicPartition{Topic: &topic, Partition: kafka.PartitionAny},
				Value:          value,
			}, nil)

			if err != nil {
				log.Printf("Failed to produce message: %v", err)
			}
		}
	}

	// Wait for messages to be delivered
	p.Flush(15 * 1000)
}
