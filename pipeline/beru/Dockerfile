# Build context must be pipeline/ so replace ../pkg/trace resolves.
#   make docker-build  →  docker build -f Dockerfile -t … ..
FROM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY pkg/trace /src/pkg/trace
COPY beru/go.mod beru/go.sum /src/beru/
WORKDIR /src/beru
RUN go mod download
COPY beru/ /src/beru/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o /beru ./cmd/beru

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /beru .
USER 65532:65532
EXPOSE 50051
ENTRYPOINT ["/beru"]
