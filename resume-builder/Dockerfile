# syntax=docker/dockerfile:1
#
# A static site: no build step, no framework, no compiler. The "build" is a
# copy, which is why there is no builder stage here and nothing to cache.
#
# amd64 only, deliberately. The cluster's managed node group is t3.medium,
# which is x86_64 - confirmed against dev-eks-us-east-1, where both nodes
# report instance-type t3.medium. The repository README used to assert that
# nodes are Graviton and that an amd64-only image "will not schedule"; that is
# not true of this cluster today. When an arm64 node pool exists, add
# linux/arm64 to the platforms list in .github/workflows/build-push.yml - this
# Dockerfile needs no change, because nothing in it is architecture-specific.

FROM nginxinc/nginx-unprivileged:1.27-alpine

# The base image already runs as uid 101. Switching to root to COPY and back
# again would leave a root-owned layer for no gain: the files only need to be
# world-readable, which is what COPY produces by default.
USER 101

# Server config first - it changes far less often than the content, so the
# content copy below is the only layer a typical edit invalidates.
COPY --chown=101:101 nginx.conf /etc/nginx/conf.d/default.conf

COPY --chown=101:101 src/ /usr/share/nginx/html/

# 8080 because a non-root process cannot bind a privileged port. The Service
# targets this; the Deployment's containerPort must match.
EXPOSE 8080

# OCI annotations tie a running image back to the commit that produced it,
# which is what a registry UI and a scanner both read.
ARG VERSION=dev
LABEL org.opencontainers.image.title="resume-builder" \
      org.opencontainers.image.description="Static resume site served by unprivileged nginx" \
      org.opencontainers.image.source="https://github.com/Ubuntu-25c-market-prep/apps-business" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.licenses="UNLICENSED" \
      io.u25c.workstream="argocd"

# Inherited from the base image, restated so it is visible here:
#   CMD ["nginx", "-g", "daemon off;"]
# nginx is PID 1 and receives SIGTERM directly, so a rollout drains rather
# than waiting out the grace period and being killed.
