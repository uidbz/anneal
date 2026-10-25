# Anneal — build, test, docs gates
GO      ?= go
BIN     := bin

.PHONY: all build test vet fmt check-ui check-script-ui headless clean live-tie-test live-m10-test live-probe-test live-m13-test live-m14-test gui-m15-smoke live-m16-test

all: build

build: anneal headless

anneal:
	$(GO) build -o $(BIN)/anneal ./cmd/anneal

headless:
	$(GO) build -o $(BIN)/anneal-headless ./cmd/anneal-headless

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

# Scripted full-UI e2e driver smoke (M15; needs a display; test home)
gui-m15-smoke: anneal
	ANNEAL_HOME=/tmp/opencode/anneal-home ANNEAL_UI_DIR=$(CURDIR)/testing/e2e/m15 ./bin/anneal -script-ui

# Live review-bridge gate (M16; task + learning pass, ~5 min; Strata up)
live-m16-test: build
	bash testing/e2e/m16/run.sh

vet:
	$(GO) vet ./...

fmt:
	gofmt -w core cmd

# Compile-check the embedded scripted pane (product UI)
check-ui: anneal
	$(BIN)/anneal -check-ui

# Compile-check the full-window scripted UI used by e2e/custom UI drivers
check-script-ui: anneal
	ANNEAL_UI_DIR=$(CURDIR)/testing/e2e/m15 $(BIN)/anneal -check-script-ui

# Recommended pre-commit gate
ci: vet test check-ui check-script-ui

clean:
	rm -rf $(BIN)
