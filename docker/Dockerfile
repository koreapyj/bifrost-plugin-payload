# Runtime image for the dynamically linked bifrost-http plus the payload plugin.
#
# It mirrors the layout of the official Bifrost image (transports/Dockerfile):
# /app/main, /app/docker-entrypoint.sh, APP_DIR=/app/data, UID 1000, port 8080,
# so the official Helm chart works with only image.repository/image.tag changed.
# The base is glibc (Ubuntu 24.04) because both binaries are built with cgo on
# ubuntu-24.04 runners; the official Alpine/musl base cannot run them.
#
# Build context (prepared by CI):
#   bifrost-http           dynamically linked Bifrost gateway
#   payload.so             the plugin, built in the same Go workspace
#   docker-entrypoint.sh   from bifrost/transports of the same tag
FROM ubuntu:24.04

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates wget && \
    rm -rf /var/lib/apt/lists/* && \
    userdel --remove ubuntu 2>/dev/null || true

WORKDIR /app

COPY bifrost-http /app/main
COPY docker-entrypoint.sh /app/docker-entrypoint.sh
COPY payload.so /app/plugins/payload.so

ARG ARG_APP_PORT=8080
ARG ARG_APP_HOST=0.0.0.0
ARG ARG_LOG_LEVEL=info
ARG ARG_LOG_STYLE=json
ARG ARG_APP_DIR=/app/data
ENV APP_PORT=$ARG_APP_PORT \
    APP_HOST=$ARG_APP_HOST \
    LOG_LEVEL=$ARG_LOG_LEVEL \
    LOG_STYLE=$ARG_LOG_STYLE \
    APP_DIR=$ARG_APP_DIR
ENV GOGC="" \
    GOMEMLIMIT=""

RUN mkdir -p "$APP_DIR/logs" && \
    useradd --uid 1000 --gid 0 --no-create-home --shell /bin/sh appuser && \
    chown -R 1000:0 /app && \
    chmod -R g=rwX "$APP_DIR" && \
    chmod 0755 /app/main /app/docker-entrypoint.sh && \
    chmod 0644 /app/plugins/payload.so

USER 1000:0
VOLUME ["${APP_DIR}"]
EXPOSE $APP_PORT

HEALTHCHECK --interval=30s --timeout=10s --start-period=5s --retries=3 \
    CMD wget -q -O /dev/null http://127.0.0.1:${APP_PORT}/health || exit 1

ENTRYPOINT ["/app/docker-entrypoint.sh"]
CMD ["/app/main"]
