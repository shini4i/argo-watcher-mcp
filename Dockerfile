FROM python:3.13-slim-bookworm@sha256:f2fdaec50160418e0c2867ba3e254755edd067171725886d5d303fd7057bbf81 AS builder

ENV PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=off

WORKDIR /app

COPY pyproject.toml poetry.lock ./
COPY src/ ./src/

RUN pip install . --no-cache-dir --prefix=/install

FROM python:3.13-slim-bookworm@sha256:f2fdaec50160418e0c2867ba3e254755edd067171725886d5d303fd7057bbf81

ENV APP_HOME=/app

RUN adduser --system --group --home ${APP_HOME} app

COPY --from=builder /install ${APP_HOME}
RUN chown -R app:app ${APP_HOME}

USER app

ENV PATH="${APP_HOME}/bin:${PATH}" \
    PYTHONPATH="${APP_HOME}/lib/python3.13/site-packages"

WORKDIR ${APP_HOME}

EXPOSE 8000

# The entrypoint explicitly uses the python interpreter to run the 'mcp' script,
# bypassing shebang issues, and provides a direct file path to the main app,
# which works around the mcp tool's flawed module discovery.
ENTRYPOINT ["python", "/app/bin/mcp", "run", "--transport", "sse", "/app/lib/python3.13/site-packages/argo_watcher_mcp/main.py"]
