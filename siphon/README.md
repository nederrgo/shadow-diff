# Siphon

Node-level traffic capture agent for Shadow-Diff. Uses Linux `AF_PACKET` (TPACKET v3) and TCP reassembly to forward production HTTP requests to Igris.

## Environment

| Variable | Default | Description |
|----------|---------|-------------|
| `SIPHON_INTERFACE` | `any` | `any`/`auto`/empty: capture on all active non-loopback interfaces; otherwise a single interface name (`eth0`, `cni0`, …) |
| `SIPHON_API_ADDR` | `:8080` | Config and health API listen address |

## API

- `GET /healthz` — liveness
- `GET /v1/status` — BPF filter string, bound interfaces, counters
- `POST /v1/config` — Monarch JSON payload (hot reload)

## Build

Requires Linux, Go 1.25+, and **CGO** (gcc) for `gopacket/afpacket`.

```bash
make build
make docker-build SIPHON_IMG=siphon:latest
```

## Deploy

```bash
kubectl apply -k deploy/
```

DaemonSet requires `hostNetwork: true` and `privileged: true`.
