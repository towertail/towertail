# syntax=docker/dockerfile:1.7
# Build context is server/ (see server/docker/docker-compose.yaml).
# The build stage runs on the host arch and cross-compiles (no emulation).
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG SHA=unknown
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
      -trimpath \
      -ldflags="-s -w -X github.com/towertail/towertail/server/internal/version.Version=${VERSION} -X github.com/towertail/towertail/server/internal/version.SHA=${SHA}" \
      -o /out/towertail-server ./cmd/towertail-server
# Empty data dir, so a new volume on /var/lib/towertail belongs to nonroot.
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/towertail-server /usr/local/bin/towertail-server
COPY --from=build --chown=nonroot:nonroot /out/data /var/lib/towertail
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/towertail-server"]
CMD ["serve"]
