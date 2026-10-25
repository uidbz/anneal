# anneal — build, test, docs gates
GO      ?= go
BIN     := bin

.PHONY: all build test vet fmt check-ui check-pane headless legacy clean live-tie-test live-m10-test live-probe-test live-m13-test live-m14-test gui-m15-smoke live-m16-test

all: build

build: anneal headless legacy
	ln -sf anneal $(BIN)/fyagent
	ln -sf anneal-headless $(BIN)/fyagent-headless
	ln -sf anneal-legacy $(BIN)/fyagent-legacy

anneal:
	$(GO) build -o $(BIN)/anneal ./cmd/anneal

headless:
	$(GO) build -o $(BIN)/anneal-headless ./cmd/anneal-headless

# Preserved all-Risor UI for e2e drivers and one-release fallback.
legacy:
	$(GO) build -o $(BIN)/anneal-legacy ./cmd/anneal-legacy

# Hermetic tests (no network; live-infrastructure tests skip unless opted in)
test:
	$(GO) test ./...

# Against the real tie triplestore (uses ~/.config/tie or ANNEAL_* env)
live-tie-test:
	ANNEAL_LIVE_TIE=1 $(GO) test ./core/tieindex -run TestLiveRoundtrip -v

# Live harness-components gate (Strata + demo project must be up)
live-m10-test: build
	bash testing/e2e/m10/run.sh

# Live probe replay gate (M11; Strata must be up)
live-probe-test:
	ANNEAL_LIVE_PROBE=1 $(GO) test ./core/probe -run TestLiveReplay -v

# Live improvement-library gate (M13; two full steps, ~40 min; Strata up)
live-m13-test: build
	bash testing/e2e/m13/run.sh

# Live k-step recursion gate (M14; one -k 2 run, ~50 min; Strata up)
live-m14-test: build
	bash testing/e2e/m14/run.sh

# Harness-panel GUI smoke (M15; needs a display; uses the test home)
gui-m15-smoke: legacy
	ANNEAL_HOME=/tmp/opencode/anneal-home ANNEAL_UI_DIR=$(CURDIR)/testing/e2e/m15 ./bin/anneal-legacy

# Live review-bridge gate (M16; task + learning pass, ~5 min; Strata up)
live-m16-test: build
	bash testing/e2e/m16/run.sh

vet:
	$(GO) vet ./...

fmt:
	gofmt -w core cmd assets 2>/dev/null || gofmt -w core cmd

# Compile-check the legacy full UI (fynerisor CompileScript)
check-ui: legacy
	$(BIN)/anneal-legacy -check-ui

# Compile-check the scripted pane embedded in the new Go shell
check-pane: anneal
	$(BIN)/anneal -check-ui

# Recommended pre-commit gate
ci: vet test check-ui check-pane

clean:
	rm -rf $(BIN)
