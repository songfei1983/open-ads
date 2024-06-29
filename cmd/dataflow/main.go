package main

import (
	"encoding/json"
	"fmt"

	"github.com/confluentinc/confluent-kafka-go/kafka"
)

const (
	KafkaServer  = "localhost:9092"
	KafkaTopic   = "words"
	KafkaGroupId = "words-service"
)

type Order struct {
	ID        string
	ProductId string
	UserId    string
	Amount    int
}

func main() {
	c, err := kafka.NewConsumer(&kafka.ConfigMap{
		"bootstrap.servers": KafkaServer,
		"group.id":          KafkaGroupId,
		"auto.offset.reset": "earliest",
	})
	if err != nil {
		panic(err)
	}
	defer c.Close()

	topic := KafkaTopic
	err = c.SubscribeTopics([]string{topic}, nil)
	if err != nil {
		panic(err)
	}

	for {
		msg, err := c.ReadMessage(-1)
		if err == nil {
			var order Order
			err := json.Unmarshal(msg.Value, &order)
			if err != nil {
				fmt.Printf("Error decoding message: %v\n", err)
				continue
			}

			fmt.Printf("Received Order: %+v\n", order)
		} else {
			fmt.Printf("Error: %v\n", err)
		}
	}
}
