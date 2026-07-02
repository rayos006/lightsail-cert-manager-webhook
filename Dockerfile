# Multi-stage build. Static binary in a minimal runtime.
FROM golang:1.23-alpine AS build

RUN apk add --no-cache git

WORKDIR /workspace
COPY . .

# go mod tidy also generates go.sum if it's missing — lets us build even
# when the repo doesn't ship go.sum (e.g. first release, before we have
# a Go install locally).
RUN go mod tidy && \
    CGO_ENABLED=0 go build -o webhook -ldflags '-w -extldflags "-static"' .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /workspace/webhook /usr/local/bin/webhook
ENTRYPOINT ["webhook"]
