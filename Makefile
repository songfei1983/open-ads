TOPIC=words
ZOOKEEPER=zoo1:2181
KAFKA=localhost:9092

RUNNER=direct-runner

DOCKER_COMPOSE=docker-compose -f ./deployments/docker-compose/docker-compose.yml

setup:
	./setup.sh

up:
	$(DOCKER_COMPOSE) up

upd:
	$(DOCKER_COMPOSE) up -d

down:
	$(DOCKER_COMPOSE) down

topic:
	$(DOCKER_COMPOSE) exec kafka1 kafka-topics --create --topic $(TOPIC) --partitions 1 --replication-factor 1 --bootstrap-server $(KAFKA)

describe:
	$(DOCKER_COMPOSE) exec kafka1 kafka-topics --describe --topic $(TOPIC) --bootstrap-server $(KAFKA)

offset:
	$(DOCKER_COMPOSE) exec kafka1 kafka-run-class kafka.tools.GetOffsetShell --broker-list $(KAFKA) --topic $(TOPIC) --time -1

pub:
	$(DOCKER_COMPOSE) exec kafka1 kafka-console-producer --bootstrap-server $(KAFKA) --topic $(TOPIC) --property "parse.key=true" --property "key.separator=:"

sub:
	$(DOCKER_COMPOSE) exec kafka1 kafka-console-consumer --bootstrap-server $(KAFKA) --topic $(TOPIC) --property "print.key=true" --from-beginning --max-messages 5

clean-docker:
	$(DOCKER_COMPOSE) rm -f

clean-files:
	rm wordcounts*
