FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /mongo-test-app .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /mongo-test-app /mongo-test-app
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/mongo-test-app"]
