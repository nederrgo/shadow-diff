# Shadow-Diff

Monorepo for differential testing on Kubernetes.

| Directory | Component | Description |
|-----------|-----------|-------------|
| [`monarch/`](monarch/) | Monarch | KubeBuilder operator — `ShadowTest` CRD, shadow Deployments, Envoy sidecars |
| [`beru/`](beru/) | Beru | gRPC differ service — receives traffic reports for 3-way comparison |
| [`igris/`](igris/) | Igris | Modular traffic engine (HTTP add-on MVP) — multicasts to control-a, control-b, candidate; deployed by Monarch |
| [`project-files/`](project-files/) | Docs | Early design notes |

**System architecture:** [ARCHITECTURE.md](ARCHITECTURE.md)

## Quick start

From the repository root:

```bash
# Monarch operator
make -C monarch test
make -C monarch deploy IMG=<registry>/monarch:<tag>

# Beru
make -C beru test
make beru-docker-build BERU_IMG=<registry>/beru:<tag>   # via monarch Makefile

# Igris (HTTP multicaster)
make -C igris test
make igris-docker-build IGRIS_IMG=<registry>/igris:<tag>
```

Most `make` targets can be run from the repo root and are forwarded to `monarch/`:

```bash
make test
make deploy IMG=<registry>/monarch:<tag>
make igris-test
make test-all    # Monarch + Beru + Igris unit tests
```

See [monarch/README.md](monarch/README.md) and [monarch/DEPLOYMENT.md](monarch/DEPLOYMENT.md) for operator deployment details.

**Verify Monarch, Beru, and Igris:** [VERIFICATION.md](VERIFICATION.md)
