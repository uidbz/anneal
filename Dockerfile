# anneald backend + default development environment.
#
# The image carries everything an agent run needs (anneald + toolchains);
# all configuration and state come from a single bind mount at /anneal
# (docs/CONTAINERS.md).

FROM golang:1.27 AS build
# Re-declare after FROM: ARGs before the first FROM are not visible in
# build stages.
ARG VERSION=dev
WORKDIR /src
COPY . .
# Defensive: development trees sometimes carry a local `replace` for
# fynerisor (unreleased GUI fixes). The server binary only needs
# fynerisor's public GUI-free core, so any such replace is dropped and
# the pinned public pseudo-version from go.mod is used. No-op on the
# committed module, which already pins the public version.
RUN go mod edit -dropreplace github.com/uidbz/fynerisor && go mod download
RUN CGO_ENABLED=0 go build -trimpath \
		-ldflags "-s -w -X main.version=${VERSION}-container" \
		-o /out/anneald ./cmd/anneald

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
		ca-certificates git ripgrep jq curl bash \
	&& rm -rf /var/lib/apt/lists/*
RUN useradd -m -u 1000 anneal
COPY --from=build /out/anneald /usr/local/bin/anneald
USER anneal
ENV ANNEAL_HOME=/anneal/state \
	ANNEAL_WORK_DIR=/anneal/work
EXPOSE 7777
ENTRYPOINT ["/usr/local/bin/anneald"]
