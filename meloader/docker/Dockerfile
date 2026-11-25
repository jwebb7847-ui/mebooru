FROM golang:1.27.4-trixie AS build
# Build from the Mebooru repository root.
WORKDIR /src/meloader
ENV GOWORK=off
COPY meloader/go.mod meloader/go.sum ./
COPY shared/go.mod /src/shared/go.mod
RUN go mod download
COPY shared /src/shared
COPY meloader/cmd ./cmd
COPY meloader/internal ./internal
COPY meloader/web ./web
COPY meloader/packaging/release-env.sh ./packaging/release-env.sh
COPY meloader/VERSION.md meloader/REPOSITORY.md meloader/DOC.md ./
RUN . ./packaging/release-env.sh && \
    CGO_ENABLED=0 go build \
      -ldflags="-s -w $LDFLAGS -X '$MOD.Package=docker'" \
      -o /monloader ./cmd/monloader

FROM python:3.13-slim-trixie
RUN pip install --no-cache-dir gallery-dl==1.32.19

COPY --from=build /monloader /usr/local/bin/monloader

RUN useradd --uid 1000 --user-group --create-home --home-dir /home/monloader --shell /usr/sbin/nologin monloader
USER 1000:1000
ENV HOME=/home/monloader \
    MONLOADER_SERVER_BIND_ADDRESS=0.0.0.0:8456 \
    MONLOADER_CONTAINER=1

EXPOSE 8456
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/usr/local/bin/monloader", "healthcheck", "-config", "/config/monloader.toml"]
ENTRYPOINT ["/usr/local/bin/monloader", "-config", "/config/monloader.toml"]
