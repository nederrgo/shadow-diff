module github.com/shadow-diff/egress-relay-rabbitmq

go 1.26.0

require (
	github.com/rabbitmq/amqp091-go v1.10.0
	github.com/shadow-diff/beruclient v0.0.0
	github.com/shadow-diff/trace v0.0.0
)

replace (
	github.com/shadow-diff/beruclient => ../pkg/beruclient
	github.com/shadow-diff/trace => ../pkg/trace
)
