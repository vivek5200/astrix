# syntax=docker/dockerfile:1

# ---- Build stage -----------------------------------------------------
# go.mod requires go 1.26, so the builder must be golang:1.26-alpine.
FROM golang:1.26-alpine AS build

WORKDIR /src

# Cache module downloads independently of source changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 produces fully static binaries: tree-sitter sits behind a
# `//go:build cgo` tag with a pure-Go fallback, so no C toolchain is needed.
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/astrix ./cmd/astrix \
 && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/astrixd ./cmd/astrixd

# ---- Runtime stage ---------------------------------------------------
# git + docker-cli let astrix/astrixd drive the sandbox: astrix shells out to
# `git diff` / `git apply` for patches, and docker-cli lets astrixd orchestrate
# sibling containers against the host's Docker daemon (mount the socket at
# /var/run/docker.sock). The image ships both binaries: ENTRYPOINT defaults to
# astrix; run the daemon with `--entrypoint astrixd`.
FROM alpine:3.20

LABEL org.opencontainers.image.source="https://github.com/vivek5200/astrix" \
      org.opencontainers.image.description="Astrix agentic control loop: verify, graph, and repair code against an Astrixfile contract" \
      org.opencontainers.image.licenses="Apache-2.0"

RUN apk add --no-cache git ca-certificates docker-cli

COPY --from=build /out/ /usr/local/bin/

ENTRYPOINT ["astrix"]
