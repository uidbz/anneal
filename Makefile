# fyagent — build, test, docs gates
GO      ?= go
BIN     := bin

.PHONY: all build test vet fmt check-ui headless clean live-tie-test live-m10-test

all: build

build: fyagent headless

fyagent:
	$(GO) build -o $(BIN)/fyagent ./cmd/fyagent

headless:
	$(GO) build -o $(BIN)/fyagent-headless ./cmd/fyagent-headless

# Hermetic tests (no network; live-infrastructure tests skip unless opted in)
test:
	$(GO) test ./...

# Against the real tie triplestore (uses ~/.config/tie or FYAGENT_* env)
live-tie-test:
	FYAGENT_LIVE_TIE=1 $(GO) test ./core/tieindex -run TestLiveRoundtrip -v

# Live harness-components gate (Strata + demo project must be up)
live-m10-test: build
	bash testing/e2e/m10/run.sh

vet:
	$(GO) vet ./...

fmt:
	gofmt -w core cmd assets 2>/dev/null || gofmt -w core cmd

# Compile-check the Risor UI without opening a window (fynerisor CompileScript)
check-ui: fyagent
	$(BIN)/fyagent -check-ui

# Recommended pre-commit gate
ci: vet test check-ui

clean:
	rm -rf $(BIN)
