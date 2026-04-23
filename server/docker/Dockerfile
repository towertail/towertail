# syntax=docker/dockerfile:1.7
# Build context is the repo root (see server/docker/docker-compose.yaml).
# We need both server/ and sampler/ modules plus go.work because the
# server imports sampler/pkg/schema via the workspace.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.work ./
COPY sampler/go.mod sampler/go.sum ./sampler/
COPY server/go.mod server/go.sum ./server/
RUN cd server && go mod download
COPY sampler ./sampler
COPY server ./server
ARG VERSION=dev
ARG SHA=unknown
RUN cd server && CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags="-s -w -X github.com/towertail/server/internal/version.Version=${VERSION} -X github.com/towertail/server/internal/version.SHA=${SHA}" \
      -o /out/towertail-server ./cmd/towertail-server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/towertail-server /usr/local/bin/towertail-server
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/towertail-server"]
CMD ["serve"]
