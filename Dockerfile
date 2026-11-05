# syntax=docker/dockerfile:1
# cosmos-tlm-bridge — one-command OpenC3 COSMOS v5 DROID_SIM demo.
# Keyless, no OpenC3 SDK, no Ruby, no cloud SDK. Python 3.11+ / Pydantic.
FROM python:3.11-slim

LABEL org.opencontainers.image.title="cosmos-tlm-bridge" \
      org.opencontainers.image.description="OpenC3 COSMOS v5 telemetry-to-microservice bridge + CI regression runner" \
      org.opencontainers.image.source="https://github.com/nakshjhaai-lgtm/cosmos-tlm-bridge" \
      org.opencontainers.image.licenses="Apache-2.0"

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    COSMOS_URL=http://localhost:2900 \
    COSMOS_SCOPE=DEFAULT \
    COSMOS_AUTH_MODE=keyless \
    COSMOS_PORT=2900 \
    PYTHONPATH=/app/src

WORKDIR /app
RUN pip install --no-cache-dir "pydantic>=2.6,<3" "PyYAML>=6.0,<7"

COPY src/ ./src/
COPY definitions/ ./definitions/
COPY suites/ ./suites/
COPY docker/ ./docker/
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod +x /usr/local/bin/entrypoint.sh && \
    mkdir -p /app/artifacts && \
    useradd --create-home --uid 10001 turion && \
    chown -R turion:turion /app
USER turion

EXPOSE 2900 8080
HEALTHCHECK --interval=2s --timeout=3s --start-period=3s --retries=15 \
  CMD python -c "import urllib.request,sys; sys.exit(0 if urllib.request.urlopen('http://127.0.0.1:2900/health',timeout=2).status==200 else 1)"

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
CMD ["serve"]
