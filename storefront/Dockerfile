# syntax=docker/dockerfile:1
#
# Multi-arch by cross-compilation, not emulation.
#
# The builder runs on the NATIVE architecture of the build machine
# (--platform=$BUILDPLATFORM) and Go cross-compiles to $TARGETARCH. The
# alternative - letting buildx run an arm64 builder under QEMU on an amd64
# runner - produces the same image perhaps eight times slower. Go needs no
# emulation to target another architecture, so paying for it is pure waste.
#
# Nodes are Graviton (arm64) by intent, though at the time of writing the
# cluster's managed node group is t3.medium/amd64 and no Karpenter NodePool
# exists yet. Building both is what makes that switch a non-event.

ARG GO_VERSION=1.23

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build

# Provided automatically by buildx. TARGETARCH is the one that matters:
# "amd64" or "arm64", which is exactly what GOARCH expects.
ARG TARGETOS
ARG TARGETARCH

# The image tag, baked in so a running pod can report which build it is.
ARG VERSION=dev

WORKDIR /src

# go.mod alone first, so the dependency layer is cached independently of source
# changes. There is no go.sum because the service has no external modules; add
# it here alongside go.mod the moment one appears.
COPY src/go.mod ./
RUN go mod download

COPY src/ ./

# Vet before build. It catches the class of mistake that compiles cleanly and
# fails at runtime - a Printf verb that does not match its argument, an
# unreachable branch - and it costs about a second.
RUN go vet ./...
RUN go test ./... -count=1

# CGO_ENABLED=0 produces a static binary with no libc dependency, which is what
# lets the runtime stage be distroless/static.
# -trimpath strips local filesystem paths, so the same source produces the same
# bytes on any machine, which is what makes the build reproducible.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/storefront .

# ---------------------------------------------------------------------------
# Runtime: distroless static, nonroot.
#
# No shell, no package manager, no busybox. There is nothing in this image for
# an attacker who gets code execution to pivot with, and nothing for a CVE
# scanner to flag that is not our own binary. The cost is that `kubectl exec`
# into this pod gives you nothing - debug with `kubectl debug` and an ephemeral
# container instead.
# ---------------------------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

ARG VERSION=dev

# OCI annotations. `revision` and `version` are what a scanner or a registry UI
# uses to tie a running image back to the commit that produced it.
LABEL org.opencontainers.image.title="storefront" \
      org.opencontainers.image.description="First business application on the u25c platform" \
      org.opencontainers.image.source="https://github.com/Ubuntu-25c-market-prep/apps-business" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="UNLICENSED" \
      io.u25c.workstream="argocd"

COPY --from=build /out/storefront /storefront

# 65532 is `nonroot` in the distroless base. Declared numerically because
# Kubernetes cannot resolve a username to a UID, so runAsNonRoot with a named
# user fails admission with "container has runAsNonRoot and image has non-numeric
# user". The Deployment sets runAsUser to the same number.
USER 65532:65532

EXPOSE 8080

# No shell form, so the binary is PID 1 and receives SIGTERM directly. The shell
# form wraps it in /bin/sh, which does not forward signals, and the graceful
# shutdown in main.go would never run - every rollout would be a 30-second wait
# followed by SIGKILL.
ENTRYPOINT ["/storefront"]
