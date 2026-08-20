# syntax=docker/dockerfile:1
# check=skip=FromPlatformFlagConstDisallowed

# Build natively and cross-compile. Foundry accepts linux/amd64 images only —
# arm64 is rejected at version-create time — so GOARCH is pinned here and the
# runtime stage pins a constant --platform to match. That is deliberate, not an
# oversight: this image has exactly one valid target. It also means a plain
# `docker build` on an arm64 workstation produces a correct image rather than an
# amd64 binary inside an arm64 base, without emulating the whole toolchain.
FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN GOWORK=off go mod download

COPY cmd/ ./cmd/

ARG VERSION=dev
ENV GOWORK=off CGO_ENABLED=0 GOOS=linux GOARCH=amd64

RUN go build -ldflags="-s -w -X main.Version=${VERSION}" \
    -o /out/foundry-runtime ./cmd/foundry-runtime/

# debian-slim, NOT distroless. Measured against a live sandbox: the identical
# binary on gcr.io/distroless/static-debian12 never becomes ready, while on
# debian-slim it answers in about two seconds. The platform needs a fuller base
# — most likely a shell, since it appears to wrap the entrypoint — and it
# reports the difference only as an opaque "session did not become ready", with
# container startup logs exposed through no API. Do not "optimize" this back to
# distroless without redeploying and invoking a real agent to check.
FROM --platform=linux/amd64 debian:12-slim

# ca-certificates for TLS to Azure OpenAI. Distroless bundled these; a slim
# base does not, and without them every model call fails at handshake.
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && apt-get clean

COPY --from=build /out/foundry-runtime /foundry-runtime

# The platform injects PORT; 8088 is the documented default the runtime falls
# back to when it is absent.
EXPOSE 8088

# No USER directive: the platform starts containers as uid 0, confirmed by
# probing a live sandbox.
ENTRYPOINT ["/foundry-runtime"]
