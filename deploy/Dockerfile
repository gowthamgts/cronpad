# DECISION: build stages run on the native builder platform and cross-compile, so the
# arm64 image doesn't build Node and Go under QEMU. web/dist is platform-independent.
FROM --platform=$BUILDPLATFORM node:26-slim AS web
RUN npm install --global pnpm@12.8.1
WORKDIR /src
COPY pnpm-workspace.yaml pnpm-lock.yaml ./
COPY web/package.json web/
COPY web/packages web/packages
COPY api api
RUN pnpm --dir web install --frozen-lockfile
COPY web web
RUN pnpm --dir web build

FROM --platform=$BUILDPLATFORM golang:1.27.3 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/dist web/dist
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X github.com/gowthamgts/cronpad/internal/version.Version=${VERSION} -X github.com/gowthamgts/cronpad/internal/version.Commit=${COMMIT} -X github.com/gowthamgts/cronpad/internal/version.BuildDate=${BUILD_DATE}" -o /out/cronpad ./cmd/cronpad
RUN mkdir /out/data && chmod 0777 /out/data

FROM litestream/litestream:0.3.13 AS litestream

# DECISION: single runtime image on Alpine (shell + curl for command sinks); replaces distroless/debian variants.
FROM alpine:3.22 AS runtime
RUN apk add --no-cache ca-certificates curl && adduser -D -H -u 65532 cronpad
COPY --from=build /out/cronpad /usr/local/bin/cronpad
COPY --from=litestream /usr/local/bin/litestream /usr/local/bin/litestream
COPY --from=build --chown=65532:65532 /out/data /data
COPY LICENSE /usr/share/licenses/cronpad/LICENSE
COPY web/packages/design/fonts/OFL.txt /usr/share/licenses/cronpad/maple-mono-OFL.txt
USER 65532:65532
ENV CRONPAD_DATABASE__PATH=/data/cronpad.db CRONPAD_DATABASE__HISTORY_PATH=/data/history.db
VOLUME ["/data"]
EXPOSE 8080
HEALTHCHECK CMD ["cronpad", "healthcheck"]
COPY deploy/litestream.yml /etc/litestream.yml
ENTRYPOINT ["cronpad", "entrypoint"]
CMD ["serve"]
