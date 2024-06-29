TOPIC=words
ZOOKEEPER=zoo1:2181
KAFKA=localhost:9092

RUNNER=direct-runner

up:
	docker-compose up

upd:
	docker-compose up -d

fup:
	docker-compose up --force-recreate

down:
	docker-compose down

topic:
	docker-compose exec kafka1 kafka-topics --create --topic $(TOPIC) --partitions 1 --replication-factor 1 --bootstrap-server $(KAFKA)

describe:
	docker-compose exec kafka1 kafka-topics --describe --topic $(TOPIC) --bootstrap-server $(KAFKA)

offset:
	docker-compose exec kafka1 kafka-run-class kafka.tools.GetOffsetShell --broker-list $(KAFKA) --topic $(TOPIC) --time -1

dump:
	docker-compose exec kafka1 kafka-console-consumer --bootstrap-server $(KAFKA) --topic $(TOPIC) --new-consumer --from-beginning --max-messages 5

clean-docker:
	docker-compose rm -f

clean-files:
	rm wordcounts*