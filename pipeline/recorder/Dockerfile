FROM golang:1.25 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod ./
RUN go mod download
COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -a -o /recorder ./cmd/recorder

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /recorder .
USER 65532:65532
EXPOSE 8080 4317
ENTRYPOINT ["/recorder"]
