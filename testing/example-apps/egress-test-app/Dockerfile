FROM golang:1.25 AS builder
ARG TARGETOS=linux
ARG TARGETARCH=amd64

WORKDIR /workspace
COPY go.mod ./
RUN go mod download
COPY . .

ENV CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}
RUN go build -trimpath -ldflags="-s -w" -o /egress-test-app .

FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /egress-test-app /egress-test-app
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/egress-test-app"]
