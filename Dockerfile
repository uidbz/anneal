# anneald backend + default development environment.
#
# The image carries everything an agent run needs (anneald + toolchains);
# all configuration and state come from a single bind mount at /anneal
# (docs/CONTAINERS.md). Built with -mod handling for the local fynerisor
# replace: the server binary only needs the public module (its GUI-free
# core package), so the replace is dropped before download.

ARG VERSION=dev
FROM golang:1.27 AS build
WORKDIR /src
COPY . .
# The local go.mod replaces fynerisor with a sibling checkout (unreleased GUI
# fixes). The server binary only needs fynerisor's public GUI-free core, so
# the replace is dropped and the pinned public pseudo-version is used.
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
