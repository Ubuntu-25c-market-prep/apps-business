# storefront

The first business application on the u25c platform. A small, stateless Go HTTP
service serving a product catalogue.

**Owner:** `@Ubuntu-25c-market-prep/argocd` · **Workstream:** `argocd` · **Wave:** 6

Its real job is to be the thing that proves the delivery path works: built here,
pushed to ECR as an immutable multi-arch tag, and promoted through
`app-dev → app-stage → app-prod` by pull requests in `gitops-argocd`. It is
deliberately boring so that when a deploy fails, the deploy is what is broken.

## Endpoints

| Path | Purpose |
|---|---|
| `GET /api/products` | The catalogue |
| `GET /api/products/{id}` | One product, or 404 |
| `GET /healthz` | Liveness — is the process alive |
| `GET /readyz` | Readiness — should traffic come here |
| `GET /metrics` | Prometheus exposition |

Prices are integer minor units (`price_minor_units: 4500` is £45.00), never
floats. There is no database — the catalogue is a static fixture, so that a
scaffolding ticket does not drag a StatefulSet, a backup policy and an IRSA role
onto its critical path.

## Configuration

All environment variables, all optional.

| Variable | Default | Notes |
|---|---|---|
| `PORT` | `8080` | |
| `LOG_LEVEL` | `info` | `debug` · `info` · `warn` · `error` |
| `DRAIN_DELAY_SECONDS` | `5` | Must be **less than** `terminationGracePeriodSeconds` |
| `U25C_WORKSTREAM` | `argocd` | Label on `storefront_build_info` |
| `U25C_OWNER` | `unset` | Label on `storefront_build_info` |

## Build

```bash
# single arch, runs go vet and go test inside the builder
docker build --build-arg VERSION=0.1.0 -t storefront:local storefront/

# both architectures, as CI does
docker buildx build --platform linux/amd64,linux/arm64 \
  --build-arg VERSION=0.1.0 --output=type=cacheonly storefront/
```

`VERSION` is baked into the binary with `-ldflags -X main.version` and is
reported by `/healthz` and by the `storefront_build_info` metric. **It should
always be the image tag**, so that a running pod can tell you which build it is
without anyone consulting a deployment log.

## Deployment contract

These are the properties `gitops-argocd` depends on. Changing one is a
cross-repo change, not a local one.

- **Listens on `8080`**, HTTP, no TLS — Istio terminates.
- **Runs as UID `65532`**, non-root, read-only root filesystem, no privilege
  escalation needed. Set `runAsUser: 65532` numerically; Kubernetes cannot
  resolve a username, and `runAsNonRoot` with a named user fails admission.
- **Handles `SIGTERM`**: readiness flips to 503 first, then the server drains,
  then it exits 0. Set `terminationGracePeriodSeconds` above
  `DRAIN_DELAY_SECONDS` + a margin — 30 is fine with the default 5.
- **Liveness must not follow readiness.** `/healthz` deliberately stays 200 while
  draining. Pointing the liveness probe at `/readyz` would make kubelet restart
  every pod during every rollout.
- **Scrape `/metrics`** on the same port.

Suggested probe configuration:

```yaml
livenessProbe:  { httpGet: { path: /healthz, port: 8080 }, periodSeconds: 10 }
readinessProbe: { httpGet: { path: /readyz,  port: 8080 }, periodSeconds: 5 }
```

## Metrics

| Metric | Type | Labels |
|---|---|---|
| `storefront_build_info` | gauge, always 1 | `version` `workstream` `owner` `goversion` `arch` |
| `storefront_uptime_seconds` | gauge | — |
| `storefront_http_requests_total` | counter | `method` `route` `status` |
| `storefront_http_request_duration_seconds` | histogram | `method` `route` `status` |

`route` is the **registered pattern**, not the request path — so
`/api/products/{id}`, never `/api/products/sku-1001`. Using the raw path would
give every product id its own time series, which is the standard way to take
down a Prometheus.

Probe endpoints are not instrumented. Their traffic is constant and would swamp
the histogram they shared.

Buckets start at 1ms rather than the client-library default of 5ms, because this
service is expected to answer in single-digit milliseconds and the default
buckets would put every request in the first one.

## Dependencies

None. `go.mod` lists no external modules, and there is no `go.sum` because there
is nothing to verify. The Prometheus text format is a documented plain-text
format, so `/metrics` is served directly — about fifty lines, in exchange for a
7MB image and a dependency graph nobody has to audit.

When the first real dependency arrives, `go mod tidy` produces a `go.sum` and it
must be committed alongside `go.mod`, and the `Dockerfile`'s `COPY src/go.mod ./`
must become `COPY src/go.mod src/go.sum ./`.

## Image

Multi-arch (`linux/amd64` + `linux/arm64`) by **cross-compilation**, not
emulation: the builder stage runs on the build machine's native architecture and
Go targets the other. Running an arm64 builder under QEMU would produce the same
image roughly eight times slower.

Runtime base is `gcr.io/distroless/static-debian12:nonroot` — no shell, no
package manager. `kubectl exec` into this pod gives you nothing; use
`kubectl debug` with an ephemeral container.

## SLO

Starting point, to be revised once there is real traffic. Owned by `@argocd`,
alert routing per the `monitoring` workstream's contract.

| Objective | Target | Window |
|---|---|---|
| Availability — non-5xx on `/api/*` | 99.5% | 30 days rolling |
| Latency — p99 on `/api/products` | < 100ms | 30 days rolling |

```promql
# availability
sum(rate(storefront_http_requests_total{status!~"5.."}[5m]))
  / sum(rate(storefront_http_requests_total[5m]))

# p99 latency
histogram_quantile(0.99,
  sum by (le) (rate(storefront_http_request_duration_seconds_bucket{route="/api/products"}[5m])))
```

## Not done yet

- **Tracing.** `apps-business/README.md` requires OpenTelemetry traces from every
  service. Not wired up here, because the OTel Collector does not exist yet —
  `gitops-flux#44` installs it. Instrument this service as part of that ticket,
  not before.
- **The image pipeline.** `.github/workflows/storefront.yml` builds and tests both
  architectures on every PR but does **not** push. Tagging, the ECR OIDC role and
  the push belong to `apps-business#2`.
