GO_PKGS := ./cmd/... ./internal/... ./bench/...
BIN     := brokemode

.PHONY: build web-build go-build install pull bench tui serve gateway verify test lint clean

## build: Vite build then static Go binary with the dashboard embedded
build: web-build go-build

web-build:
	cd web && npm ci && npm run build

go-build:
	CGO_ENABLED=0 go build -trimpath -o $(BIN) ./cmd/brokemode

## install: run the installer from this checkout (builds with Go)
install:
	./install.sh

## pull: ollama pull every model marked default: true in models.yaml
pull:
	@awk '/^  - name:/{name=$$3; def="false"} /^    default: true/{def="true"; print name}' models.yaml \
	  | while read -r m; do echo "pulling $$m"; ollama pull "$$m"; done

bench: go-build
	./$(BIN) bench

tui: go-build
	./$(BIN) tui

serve: build
	./$(BIN) serve

gateway: go-build
	./$(BIN) gateway

## verify: curl smoke test against a running gateway
verify:
	./scripts/verify-gateway.sh

test:
	go test $(GO_PKGS)

lint:
	golangci-lint run $(GO_PKGS)
	cd web && npm run lint && npm run format:check

clean:
	rm -rf $(BIN) results web/dist web/node_modules
