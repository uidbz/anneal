# Anneal — build, test, docs gates
GO      ?= go
BIN     := bin

# User-local by default; `make install PREFIX=/usr/local` for a system install.
PREFIX  ?= $(HOME)/.local
DESTDIR ?=
INSTALL ?= install
BINDIR  ?= $(PREFIX)/bin
DATADIR ?= $(PREFIX)/share
APPDIR  ?= $(DATADIR)/applications
ICONDIR ?= $(DATADIR)/icons/hicolor/scalable/apps

.PHONY: all build test vet fmt check-ui check-script-ui headless anneald docker clean install uninstall live-tie-test live-m10-test live-probe-test live-m13-test live-m14-test gui-m15-smoke live-m16-test

all: build

build: anneal headless anneald

anneal:
	$(GO) build -o $(BIN)/anneal ./cmd/anneal

headless:
	$(GO) build -o $(BIN)/anneal-headless ./cmd/anneal-headless

anneald:
	$(GO) build -o $(BIN)/anneald ./cmd/anneald

# Container image (docs/CONTAINERS.md): backend + default dev toolchain.
docker:
	docker build -t anneal:dev --build-arg VERSION=$(shell git describe --always --dirty 2>/dev/null || echo dev) .

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

# Install binaries + XDG desktop entry/icon (user-local by default).
install: build
	$(INSTALL) -d $(DESTDIR)$(BINDIR) $(DESTDIR)$(APPDIR) $(DESTDIR)$(ICONDIR)
	$(INSTALL) -m 0755 $(BIN)/anneal $(DESTDIR)$(BINDIR)/anneal
	$(INSTALL) -m 0755 $(BIN)/anneal-headless $(DESTDIR)$(BINDIR)/anneal-headless
	$(INSTALL) -m 0755 $(BIN)/anneald $(DESTDIR)$(BINDIR)/anneald
	$(INSTALL) -m 0644 assets/anneal.desktop $(DESTDIR)$(APPDIR)/anneal.desktop
	$(INSTALL) -m 0644 assets/anneal.svg $(DESTDIR)$(ICONDIR)/anneal.svg
	@if [ -z "$(DESTDIR)" ] && command -v update-desktop-database >/dev/null 2>&1; then \
		update-desktop-database $(APPDIR); \
	fi
	@if [ -z "$(DESTDIR)" ] && command -v gtk-update-icon-cache >/dev/null 2>&1; then \
		gtk-update-icon-cache -q -t $(DATADIR)/icons/hicolor || true; \
	fi
	@echo "installed anneal -> $(BINDIR)/anneal"
	@echo "desktop entry -> $(APPDIR)/anneal.desktop"

uninstall:
	rm -f $(DESTDIR)$(BINDIR)/anneal $(DESTDIR)$(BINDIR)/anneal-headless $(DESTDIR)$(BINDIR)/anneald
	rm -f $(DESTDIR)$(APPDIR)/anneal.desktop $(DESTDIR)$(ICONDIR)/anneal.svg
	@echo "uninstalled anneal"

# Recommended pre-commit gate
ci: vet test check-ui check-script-ui

clean:
	rm -rf $(BIN)
