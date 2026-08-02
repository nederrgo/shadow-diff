# Build context must be pipeline/ (parent of tusk/) so replace ../pkg/monarchpb resolves.
#   make docker-build  →  docker build -f Dockerfile -t … ..
FROM golang:1.26-alpine AS builder
WORKDIR /src
COPY pkg/monarchpb /src/pkg/monarchpb
COPY tusk/go.mod tusk/go.sum /src/tusk/
WORKDIR /src/tusk
RUN go mod download
COPY tusk/ /src/tusk/
RUN CGO_ENABLED=0 go build -o /tusk ./cmd/tusk

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /tusk /tusk
USER 65532:65532
EXPOSE 8082
ENTRYPOINT ["/tusk"]
