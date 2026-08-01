# ── Stage 1: build ───────────────────────────────────────────────────────────
# Builds a static, stripped, reproducible binary. The CA bundle is installed
# here so the minimal runtime stage can reuse it for HTTPS to Gemini.
FROM golang:1.26-alpine AS build

RUN apk add --no-cache ca-certificates

WORKDIR /build
COPY go.mod ./
COPY src/ ./src/
COPY .env.example ./

RUN CGO_ENABLED=0 go build \
    -trimpath \
    -buildvcs=false \
    -ldflags="-s -w" \
    -o /out/gemini-web2api \
    ./src

# Fail-closed default: the baked .env forces auth to be required, so the
# image never serves an open API unless the operator explicitly overrides it
# (real env vars / --env-file always win over this baked file at runtime).
RUN sed 's/^GEMINI_REQUIRE_AUTH=.*/GEMINI_REQUIRE_AUTH=true/' .env.example > /out/.env

# ── Stage 2: runtime ─────────────────────────────────────────────────────────
# scratch: no shell, no package manager, no extra tools — the smallest
# possible attack surface. Contains only the static binary, the CA bundle and
# the default config. Runs as the unprivileged "nobody" user (UID/GID 65534).
FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/gemini-web2api /gemini-web2api
# .env is the single source of truth; the example file is baked in as the
# default (real env vars / --env-file override it at runtime).
COPY --from=build /out/.env /app/.env

WORKDIR /app
USER 65534:65534

EXPOSE 8081

# Health probe uses the binary itself (no shell available in scratch).
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD ["/gemini-web2api", "--check"]

ENTRYPOINT ["/gemini-web2api"]
