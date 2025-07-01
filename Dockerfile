# ---- Builder Stage ----
# This stage builds our dependencies and application source.
FROM python:3.13-slim-bookworm@sha256:f2fdaec50160418e0c2867ba3e254755edd067171725886d5d303fd7057bbf81 AS builder

# Set Python environment variables for production
ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    PIP_NO_CACHE_DIR=off

# Install poetry, which will be used to export dependencies
RUN pip install poetry
WORKDIR /app

# Copy dependency definition files
COPY pyproject.toml poetry.lock ./

# Export dependencies to requirements.txt for optimized caching. This step
# ensures that our dependency installation below doesn't depend on the app's source code.
RUN poetry export -f requirements.txt --output requirements.txt --without-hashes

# Install only the third-party dependencies from requirements.txt.
# This creates a durable cache layer that only invalidates if the lock file changes.
RUN pip install -r requirements.txt

# Copy the application source code
COPY src ./src
COPY server.py .

# Install the application source code itself. This is a fast operation
# because all heavy dependencies are already installed from the cached layer above.
RUN pip install .


# ---- Final Stage ----
# This stage creates the final, lean, and secure image for production.
FROM python:3.13-slim-bookworm@sha256:f2fdaec50160418e0c2867ba3e254755edd067171725886d5d303fd7057bbf81

# Set environment variables again for the final stage
ENV PYTHONUNBUFFERED=1 \
    PYTHONDONTWRITEBYTECODE=1 \
    APP_HOME=/app

# Create a non-root user for security
RUN adduser --system --group --home ${APP_HOME} app

WORKDIR ${APP_HOME}

# Copy the installed packages and application script from the builder stage.
# The user and group 'app' is set as the owner of the copied files.
COPY --from=builder --chown=app:app /usr/local/lib/python3.13/site-packages /usr/local/lib/python3.13/site-packages
COPY --from=builder --chown=app:app /app/server.py .

# Switch to the non-root user before running the app
USER app

EXPOSE 8000

# Use Gunicorn to run multiple Uvicorn workers for production performance.
CMD ["gunicorn", "--bind", "0.0.0.0:8000", "-w", "4", "-k", "uvicorn.workers.UvicornWorker", "argo_watcher_mcp.app:create_app"]
