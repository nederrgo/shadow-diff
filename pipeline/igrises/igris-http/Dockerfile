# Build context must be pipeline/ so replace ../../pkg/s3utils resolves.
#   make docker-build  →  docker build -f igrises/igris-http/Dockerfile -t … .
# (Makefile sets context to pipeline/ via ../..)
FROM golang:1.26 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
COPY pkg/s3utils /src/pkg/s3utils
COPY igrises/igris-http/go.mod igrises/igris-http/go.sum /src/igrises/igris-http/
WORKDIR /src/igrises/igris-http
RUN go mod download
COPY igrises/igris-http/ /src/igrises/igris-http/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o /igris ./cmd/igris

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /igris .
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/igris"]
