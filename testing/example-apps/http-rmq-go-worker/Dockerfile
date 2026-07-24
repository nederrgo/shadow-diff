FROM golang:1.25 AS builder
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go mod tidy && CGO_ENABLED=0 go build -o /http-rmq-go-worker .

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /http-rmq-go-worker /http-rmq-go-worker
USER 65532:65532
ENTRYPOINT ["/http-rmq-go-worker"]
