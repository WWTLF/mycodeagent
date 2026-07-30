APP_NAME := mycodeagent
CMD := ./cmd/$(APP_NAME)
DIST := dist

# Stamped into the binary so `mycodeagent --version` names the exact commit a
# user is running. Falls back to "dev" outside a git checkout.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# Platforms that build from the current sources unmodified. windows/amd64 is
# absent on purpose: internal/infrastructure/ssh/tunnel.go uses
# SysProcAttr.Setpgid and syscall.Kill, neither of which exists on Windows.
# Supporting it means splitting those two calls into build-tagged files.
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: build run test check clean release checksums

build:
	go build -ldflags "$(LDFLAGS)" -o $(APP_NAME) $(CMD)

run:
	go run $(CMD)

test:
	go test ./...

# The pre-commit gate from CLAUDE.md. `release` depends on it so a broken tree
# cannot be shipped by accident.
check:
	go vet ./...
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	go test -race ./...

release: check
	@rm -rf $(DIST) && mkdir -p $(DIST)
	@for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; \
		name=$(APP_NAME)_$(VERSION)_$${os}_$${arch}; \
		echo "  building $$os/$$arch"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 \
			go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$$name $(CMD) || exit 1; \
		( cd $(DIST) && tar czf $$name.tar.gz $$name && rm $$name ); \
	done
	@$(MAKE) --no-print-directory checksums
	@echo "  -> $(DIST)/ ($(VERSION))"

checksums:
	@cd $(DIST) && sha256sum *.tar.gz > SHA256SUMS && cat SHA256SUMS

clean:
	rm -f $(APP_NAME)
	rm -rf $(DIST)
