FROM gcr.io/distroless/static:nonroot

WORKDIR /app

COPY --chown=nonroot:nonroot argo-watcher-mcp /usr/local/bin/argo-watcher-mcp

ENTRYPOINT ["/usr/local/bin/argo-watcher-mcp"]
