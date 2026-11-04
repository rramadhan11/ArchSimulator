# ArchSim server image: the web app (with the WebAssembly engine) served by
# apps/api behind the single-user login. Build from the repo root:
#
#   docker build -t archsim:local .
#
# deploy/docker-compose.yml builds and runs it; see deploy/README.md.

# The engine compiled to WebAssembly, plus the wasm_exec.js of the same Go.
FROM golang:1.23.8-bookworm AS wasm
WORKDIR /src
COPY engine/ engine/
RUN GOOS=js GOARCH=wasm go build -C engine -trimpath -ldflags="-s -w" -o /out/engine.wasm ./cmd/wasm \
 && cp "$(go env GOROOT)/misc/wasm/wasm_exec.js" /out/wasm_exec.js

FROM node:22.12.0-bookworm-slim AS web
WORKDIR /src
COPY apps/web/package.json apps/web/package-lock.json apps/web/
RUN cd apps/web && npm ci
# Templates are imported from engine/examples.
COPY engine/examples/ engine/examples/
COPY apps/web/ apps/web/
COPY --from=wasm /out/engine.wasm apps/web/public/engine.wasm
COPY --from=wasm /out/wasm_exec.js apps/web/src/engine/wasm_exec.js
RUN cd apps/web && npm run build \
 && gzip -9 -k dist/engine.wasm dist/assets/*.js dist/assets/*.css

FROM golang:1.23.8-bookworm AS api
WORKDIR /src
COPY apps/api/go.mod apps/api/go.sum apps/api/
RUN cd apps/api && go mod download
COPY apps/api/ apps/api/
RUN cd apps/api && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/archsim-api . \
 && mkdir /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=api /out/archsim-api /app/archsim-api
COPY --from=web /src/apps/web/dist /app/web
# Mount point for saved designs, writable by the nonroot user (65532).
COPY --from=api --chown=65532:65532 /out/data /data
ENV ARCHSIM_ADDR=:8080 \
    ARCHSIM_WEB_DIR=/app/web \
    ARCHSIM_DATA_DIR=/data
EXPOSE 8080
ENTRYPOINT ["/app/archsim-api"]
