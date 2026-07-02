# Multi-stage build. The build stage always runs on the native build host
# ($BUILDPLATFORM) and cross-compiles for $TARGETARCH, so arm64 images
# don't pay the QEMU emulation tax. Runtime is distroless/static: no
# package installs, CA certs included. Root variant because the webhook
# binds :443.
FROM --platform=$BUILDPLATFORM golang:1.23-alpine AS build

WORKDIR /workspace

# Modules in their own layer so source changes don't re-download them.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -o webhook -ldflags '-s -w' .

FROM gcr.io/distroless/static:latest
COPY --from=build /workspace/webhook /usr/local/bin/webhook
ENTRYPOINT ["webhook"]
