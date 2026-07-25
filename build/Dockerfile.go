# Shared build for the Go components. The COMPONENT arg selects which cmd/ to
# build, so planner and ui-backend cannot drift in base image or hardening.
#
#   docker buildx build -f build/Dockerfile.go --build-arg COMPONENT=planner .
#
# syntax=docker/dockerfile:1

ARG GO_VERSION=1.26

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build

WORKDIR /src

# Dependencies first so a source-only change reuses the module layer.
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

ARG COMPONENT
ARG VERSION=dev
ARG TARGETOS
ARG TARGETARCH

# CGO off keeps the binary static, which is what distroless/static and
# readOnlyRootFilesystem both require.
RUN --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/app \
      ./cmd/${COMPONENT}

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/app /app

# 65532 is distroless' nonroot user; the chart asserts the same uid so a base
# image change cannot silently drop the pod to root.
USER 65532:65532

ENTRYPOINT ["/app"]
