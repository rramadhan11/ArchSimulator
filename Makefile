# ArchSim build entry points. Requires Go (see go.work) and Node 20+.
GO   ?= go
WEB  := apps/web
WASM := $(WEB)/public/engine.wasm

.PHONY: test wasm web build run-api dev lab lab-clean clean

## test: engine + API (Go) and web unit tests
test:
	$(GO) test ./engine/... ./apps/api/... ./lab/...
	cd $(WEB) && npm test

## wasm: compile the engine to WebAssembly for the web app
wasm:
	GOOS=js GOARCH=wasm $(GO) build -C engine -trimpath -ldflags="-s -w" -o ../$(WASM) ./cmd/wasm
	@# wasm_exec.js must match the Go toolchain that built engine.wasm (Go 1.24 moved it to lib/).
	@root=$$($(GO) env GOROOT); src=$$root/lib/wasm/wasm_exec.js; [ -f $$src ] || src=$$root/misc/wasm/wasm_exec.js; \
		cp $$src $(WEB)/src/engine/wasm_exec.js

## web: production build of the web app (dist/ is served by apps/api)
web: wasm
	cd $(WEB) && npm ci && npm run build
	@# Precompressed copies; the API serves them to clients that accept gzip.
	gzip -9 -k -f $(WEB)/dist/engine.wasm $(WEB)/dist/assets/*.js $(WEB)/dist/assets/*.css

## build: web app + API binary in bin/
build: web
	$(GO) build -trimpath -o bin/archsim-api ./apps/api
	$(GO) build -trimpath -o bin/archsim ./engine/cmd/archsim

## run-api: serve the built app behind login (reads .env)
run-api:
	set -a && . ./.env && set +a && $(GO) run ./apps/api

## dev: Vite dev server with hot reload (no login; local only)
dev: wasm
	cd $(WEB) && npm run dev

## lab: calibration experiment in a local kind cluster (asks first; see lab/README.md)
lab:
	lab/run.sh

## lab-clean: delete the calibration lab's kind cluster
lab-clean:
	lab/run.sh clean

clean:
	rm -rf bin $(WEB)/dist $(WASM) $(WEB)/src/engine/wasm_exec.js
