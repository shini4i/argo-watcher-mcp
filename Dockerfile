FROM python:3.13-slim-bookworm@sha256:f2fdaec50160418e0c2867ba3e254755edd067171725886d5d303fd7057bbf81 AS builder

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PIP_NO_CACHE_DIR=1

RUN pip install --no-cache-dir poetry==2.1.2 && \
    poetry self add poetry-plugin-export
WORKDIR /app

COPY pyproject.toml poetry.lock ./

RUN poetry export -f requirements.txt --output requirements.txt --without-hashes && \
    pip install --no-cache-dir -r requirements.txt

COPY src ./src

RUN pip install --no-cache-dir .


FROM python:3.13-slim-bookworm@sha256:f2fdaec50160418e0c2867ba3e254755edd067171725886d5d303fd7057bbf81

ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    APP_HOME=/app

RUN adduser --system --group --home ${APP_HOME} app

WORKDIR ${APP_HOME}

COPY --from=builder --chown=app:app /usr/local/lib/python3.13/site-packages /usr/local/lib/python3.13/site-packages
COPY --from=builder --chown=app:app /usr/local/bin /usr/local/bin

# Switch to the non-root user.
USER app

EXPOSE 8000

CMD ["gunicorn", "--bind", "0.0.0.0:8000", "-w", "4", "-k", "uvicorn.workers.UvicornWorker", "argo_watcher_mcp.app:create_app"]
