# syntax=docker/dockerfile:1.7

FROM node:22-alpine AS webui-build
WORKDIR /src/webui
COPY webui/package.json webui/package-lock.json ./
RUN npm ci
WORKDIR /src
COPY docs/openapi.yaml ./docs/openapi.yaml
COPY webui ./webui
RUN cd webui && npm run api:types && npm run typecheck && npm run build

FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS go-build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=webui-build /src/internal/api/dashboard/static/web ./internal/api/dashboard/static/web
RUN mkdir -p /out/data \
    && CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
       go build -trimpath -ldflags="-s -w -X main.buildVersion=${VERSION}" \
       -o /out/auto-router ./cmd/auto-router \
    && chown -R 65532:65532 /out/data

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=go-build --chown=65532:65532 /out/auto-router /app/auto-router
COPY --from=go-build --chown=65532:65532 /out/data /app/data
USER 65532:65532
EXPOSE 8080
STOPSIGNAL SIGTERM
ENTRYPOINT ["/app/auto-router", "-listen", "0.0.0.0:8080"]
