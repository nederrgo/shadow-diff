# Shadow-Diff

Monorepo for differential testing on Kubernetes.

| Directory | Component | Description |
|-----------|-----------|-------------|
| [`monarch/`](monarch/) | Monarch | KubeBuilder operator — `ShadowTest` CRD, shadow Deployments, Envoy sidecars |
| [`beru/`](beru/) | Beru | gRPC differ service — receives traffic reports for 3-way comparison |
| [`project-files/`](project-files/) | Docs | Architecture notes |

## Quick start

From the repository root:

```bash
# Monarch operator
make -C monarch test
make -C monarch deploy IMG=<registry>/monarch:<tag>

# Beru
make -C beru test
make beru-docker-build BERU_IMG=<registry>/beru:<tag>   # via monarch Makefile
```

Most `make` targets can be run from the repo root and are forwarded to `monarch/`:

```bash
make test
make deploy IMG=<registry>/monarch:<tag>
```

See [monarch/README.md](monarch/README.md) and [monarch/DEPLOYMENT.md](monarch/DEPLOYMENT.md) for operator deployment details.

**Verify Monarch + Beru on your cluster:** [VERIFICATION.md](VERIFICATION.md)
