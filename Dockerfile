# scalpmcp — a Model Context Protocol server that buys data per call over x402.
#
# WHY THIS FILE EXISTS WHEN CI DOES NOT USE IT
#   Releases are built with ko, which is daemonless and needs no Dockerfile, and
#   publish to ghcr.io/dv1-321/scalpstream-mcp. Registries that build from source
#   rather than pull a published image do need one. This produces the same
#   artefact by the ordinary route: same static binary, same entrypoint, same
#   nonroot runtime. If you change one, change the other.
#
# NO SECRETS, EVER
#   There is deliberately no ARG or ENV for EVM_BASE_PRIVATE_KEY. A build
#   argument is readable forever in `docker history`, so a key must only ever
#   arrive at run time via `-e`. With no key the server starts in preview-only
#   mode and still answers introspection and every tool call, returning the free
#   preview and the exact quoted price — which is all an automated check needs,
#   and means no wallet is required to verify this image.
#
#   docker build -t scalpmcp .
#   docker run -i --rm scalpmcp            # preview-only
#   docker run -i --rm -e EVM_BASE_PRIVATE_KEY=0x... scalpmcp

# Pinned by digest, not tag: a tag is mutable, so `golang:1.26-alpine` alone
# means a rebuild months from now silently compiles against something else.
FROM golang:1.26-alpine@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS build

WORKDIR /src

# Dependencies in their own layer, so editing source does not re-download the
# module graph on every build.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 produces a static binary, which is what allows the runtime stage
# to carry no libc at all. -trimpath keeps local build paths out of the binary,
# -mod=readonly fails the build rather than quietly editing go.mod, and -s -w
# drop the symbol and DWARF tables.
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath -mod=readonly -ldflags='-s -w' \
        -o /out/scalpmcp ./cmd/scalpmcp

# distroless/static has no shell, no package manager and no writable filesystem
# to speak of: there is nothing to exec even if the process were subverted. The
# CA bundle it ships is not optional — every tool call is HTTPS.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35

COPY --from=build /out/scalpmcp /scalpmcp

# MCP speaks over this process's stdin and stdout. There is therefore no port to
# EXPOSE, and no HEALTHCHECK is possible: anything written to stdout that is not
# JSON-RPC corrupts the stream and the client drops the session. Run with -i, or
# the container gets no stdin and exits on EOF the instant it starts.
USER nonroot:nonroot
ENTRYPOINT ["/scalpmcp"]
