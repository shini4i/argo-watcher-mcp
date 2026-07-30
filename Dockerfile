FROM gcr.io/distroless/static:nonroot

WORKDIR /app

# dockers_v2 stages one binary per platform under <os>/<arch>/, not at the
# context root. buildx sets TARGETPLATFORM; plain docker build must pass it.
ARG TARGETPLATFORM
COPY --chown=nonroot:nonroot $TARGETPLATFORM/argo-watcher-mcp /usr/local/bin/argo-watcher-mcp

ENTRYPOINT ["/usr/local/bin/argo-watcher-mcp"]
