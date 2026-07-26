module github.com/shadow-diff/igris-rabbitmq

go 1.26.0

require (
	github.com/rabbitmq/amqp091-go v1.10.0
	github.com/shadow-diff/sample v0.0.0
)

replace github.com/shadow-diff/sample => ../../pkg/sample
