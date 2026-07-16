ARG GO_VERSION=1.26.5
ARG ALPINE_VERSION=3.23.5

FROM golang:${GO_VERSION}-alpine3.23 AS build

WORKDIR /src

COPY services/core/go.mod services/core/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY services/core/ ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" \
      -o /out/paper-hub-api ./cmd/api && \
    CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" \
      -o /out/paper-hub-worker ./cmd/worker && \
    CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags="-s -w" \
      -o /out/paper-hub-migrate ./cmd/migrate

FROM alpine:${ALPINE_VERSION} AS runtime

RUN apk add --no-cache ca-certificates tzdata && \
    addgroup -S -g 10001 paperhub && \
    adduser -S -D -H -u 10001 -G paperhub paperhub

WORKDIR /app

COPY --from=build --chown=paperhub:paperhub /out/paper-hub-api /app/paper-hub-api
COPY --from=build --chown=paperhub:paperhub /out/paper-hub-worker /app/paper-hub-worker
COPY --from=build --chown=paperhub:paperhub /out/paper-hub-migrate /app/paper-hub-migrate

USER 10001:10001

EXPOSE 8080

CMD ["/app/paper-hub-api"]
