# Build context must be pipeline/ so replace ../pkg/{beruclient,trace} resolves.
#   make docker-build  →  docker build -f Dockerfile -t … ..
FROM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY pkg/beruclient /src/pkg/beruclient
COPY pkg/trace /src/pkg/trace
COPY egress-relay-rabbitmq/go.mod egress-relay-rabbitmq/go.sum /src/egress-relay-rabbitmq/
WORKDIR /src/egress-relay-rabbitmq
RUN go mod download
COPY egress-relay-rabbitmq/ /src/egress-relay-rabbitmq/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o /egress-relay-rabbitmq ./cmd/egress-relay-rabbitmq

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /egress-relay-rabbitmq .
USER 65532:65532
ENTRYPOINT ["/egress-relay-rabbitmq"]
