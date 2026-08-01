#!/usr/bin/env bash
#
# deploy.sh — one-shot deployment of gemini-web2api on a Linux VPS.
#
# Designed for a fresh Ubuntu/Debian VPS. It:
#   1. installs Docker (via the official Docker repo) if missing
#   2. creates /opt/gemini-web2api with .env (the single source of truth)
#   3. asks for a GEMINI_API_KEY and forces GEMINI_REQUIRE_AUTH=true
#      (the server refuses to start without keys — fail closed)
#   4. builds the hardened two-stage image
#   5. runs the container with runtime hardening, bound to 127.0.0.1
#   6. with --domain, creates an nginx vhost (sites-available + symlink),
#      terminates TLS via certbot and opens ports 80/443 in ufw
#
# The container listens on 127.0.0.1 only; nginx (or your own reverse
# proxy / Cloudflare Tunnel) is the public entry point.
#
# Usage:
#   sudo ./deploy.sh                            # local-only, 127.0.0.1:8081
#   sudo ./deploy.sh --domain api.example.com   # + nginx vhost + TLS (certbot)
#   sudo ./deploy.sh --domain api.example.com --port 9000
#   sudo ./deploy.sh --help
#
# Idempotent: re-run after `git pull` to rebuild and restart; config in
# /opt/gemini-web2api is preserved.
#
set -euo pipefail

# ── defaults ──────────────────────────────────────────────────────────────────
APP_NAME="gemini-web2api"
APP_DIR="/opt/gemini-web2api"
IMG_NAME="gemini-web2api:local"
HOST_PORT=8081
DOMAIN=""
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

info()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()    { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn()  { printf '\033[1;33m==>\033[0m %s\n' "$*" >&2; }
die()   { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
    cat <<'EOF'
deploy.sh — one-shot deployment of gemini-web2api on a Linux VPS.

Usage:
  sudo ./deploy.sh                            # local-only, 127.0.0.1:8081
  sudo ./deploy.sh --domain api.example.com   # + nginx vhost + TLS (certbot)
  sudo ./deploy.sh --domain api.example.com --port 9000
  sudo ./deploy.sh --help

Options:
  --domain DOMAIN   create an nginx vhost in front and issue a TLS cert via
                    certbot (Let's Encrypt, ECDSA keys to match this server)
  --port PORT       host port to publish (default 8081)
  --help            show this help

The container binds to 127.0.0.1 only; nginx (or your own proxy / Cloudflare
Tunnel) is the public entry point. Re-running the script after `git pull`
rebuilds and restarts the container while keeping /opt/gemini-web2api config.
EOF
    exit 0
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --domain) DOMAIN="${2:-}"; shift 2 ;;
        --port)   HOST_PORT="${2:-8081}"; shift 2 ;;
        --help|-h) usage ;;
        *) die "unknown option: $1 (see --help)" ;;
    esac
done

[[ "$(id -u)" -eq 0 ]] || die "run as root: sudo $0"

# ── 1. Docker ─────────────────────────────────────────────────────────────────
install_docker() {
    if command -v docker >/dev/null 2>&1; then
        ok "docker already installed ($(docker --version 2>/dev/null | awk '{print $3}' | tr -d ','))"
        return
    fi
    info "installing docker..."
    . /etc/os-release
    [[ "$ID" == "ubuntu" || "$ID" == "debian" ]] || die "unsupported distro '$ID'; install Docker manually"
    apt-get update -qq
    apt-get install -y -qq ca-certificates curl >/dev/null
    install -m 0755 -d /etc/apt/keyrings
    curl -fsSL "https://download.docker.com/linux/$ID/gpg" -o /etc/apt/keyrings/docker.asc
    chmod a+r /etc/apt/keyrings/docker.asc
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/$ID $VERSION_CODENAME stable" \
        > /etc/apt/sources.list.d/docker.list
    apt-get update -qq
    apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin >/dev/null
    systemctl enable --now docker >/dev/null
    ok "docker installed"
}

# ── 2. .env ───────────────────────────────────────────────────────────────────
prepare_config() {
    mkdir -p "$APP_DIR"

    if [[ ! -f "$APP_DIR/.env" ]]; then
        cp "$SCRIPT_DIR/.env.example" "$APP_DIR/.env"
        info "created $APP_DIR/.env (from .env.example)"
    else
        ok ".env already exists: $APP_DIR/.env"
    fi
}

# Set GEMINI_API_KEY (if unset) and force GEMINI_REQUIRE_AUTH=true.
# This is the fail-closed guardrail for public deployments.
ensure_auth() {
    local env_file="$APP_DIR/.env"
    local current
    current="$(grep -E '^GEMINI_API_KEY=' "$env_file" | tail -1 | cut -d= -f2- || true)"

    if [[ -z "$current" || "$current" == "sk-your-key" ]]; then
        if [[ -t 0 ]]; then
            read -r -p "GEMINI_API_KEY (clients must send this as Bearer): " key
        else
            key="sk-$(head -c 18 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c 24)"
            info "no TTY — generated an API key: $key"
        fi
        [[ -n "$key" ]] || die "GEMINI_API_KEY is required for a public deployment"
        # Escape sed replacement metacharacters so user-supplied keys are safe.
        key_escaped="$(printf '%s' "$key" | sed 's/[&\\|]/\\&/g')"
        if grep -qE '^#?GEMINI_API_KEY=' "$env_file"; then
            sed -i "s|^#\?GEMINI_API_KEY=.*|GEMINI_API_KEY=$key_escaped|" "$env_file"
        else
            echo "GEMINI_API_KEY=$key" >> "$env_file"
        fi
        ok "GEMINI_API_KEY set"
    else
        ok "GEMINI_API_KEY already set"
    fi

    if grep -qE '^#?GEMINI_REQUIRE_AUTH=' "$env_file"; then
        sed -i "s|^#\?GEMINI_REQUIRE_AUTH=.*|GEMINI_REQUIRE_AUTH=true|" "$env_file"
    else
        echo "GEMINI_REQUIRE_AUTH=true" >> "$env_file"
    fi
    ok "GEMINI_REQUIRE_AUTH=true (fail closed)"
}

# ── 3. build & run ────────────────────────────────────────────────────────────
build_and_run() {
    info "building hardened image (this can take a few minutes)..."
    docker build -t "$IMG_NAME" "$SCRIPT_DIR" || die "docker build failed"

    info "removing old container (if any)..."
    docker rm -f "$APP_NAME" >/dev/null 2>&1 || true

    info "starting container..."
    docker run -d \
        --name "$APP_NAME" \
        --restart unless-stopped \
        -p "127.0.0.1:${HOST_PORT}:8081" \
        --read-only \
        --tmpfs /tmp \
        --cap-drop=ALL \
        --security-opt no-new-privileges \
        --env-file "$APP_DIR/.env" \
        "$IMG_NAME" >/dev/null
    ok "container started (127.0.0.1:${HOST_PORT} -> container 8081)"
}

wait_healthy() {
    info "waiting for health check..."
    for _ in $(seq 1 30); do
        status="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}unknown{{end}}' "$APP_NAME" 2>/dev/null || true)"
        [[ "$status" == "healthy" ]] && { ok "health check: healthy"; return 0; }
        sleep 2
    done
    warn "container not healthy after 60s — check logs: docker logs $APP_NAME"
}

# ── 4. nginx reverse proxy with automatic HTTPS ──────────────────────────────
install_nginx_certbot() {
    if command -v nginx >/dev/null 2>&1 && command -v certbot >/dev/null 2>&1; then
        ok "nginx + certbot already installed"
        return
    fi
    info "installing nginx + certbot..."
    apt-get update -qq
    apt-get install -y -qq nginx certbot python3-certbot-nginx >/dev/null
    ok "nginx + certbot installed"
}

# HTTP-only vhost: serves the ACME webroot challenge and proxies / to the app.
# Used before a TLS cert exists; setup_tls rewrites the config to HTTPS after
# issuance, so this is never the final state for a domain.
setup_nginx() {
    [[ -n "$DOMAIN" ]] || return 0
    install_nginx_certbot
    mkdir -p /var/www/_acme_challenge

    local conf="/etc/nginx/sites-available/${DOMAIN}.conf"
    cat > "$conf" <<EOF
# gemini-web2api reverse proxy for $DOMAIN (generated by deploy.sh)
# The app listens on 127.0.0.1:$HOST_PORT only — never expose it directly.

server {
    listen 80;
    listen [::]:80;
    server_name $DOMAIN;

    location /.well-known/acme-challenge/ {
        root /var/www/_acme_challenge;
        default_type text/plain;
        try_files \$uri =404;
    }

    location / {
        proxy_pass http://127.0.0.1:$HOST_PORT;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        # SSE streaming (chat completions) must not be buffered.
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 300s;
        proxy_send_timeout 300s;
    }
}
EOF
    ln -sf "$conf" "/etc/nginx/sites-enabled/${DOMAIN}.conf"
    nginx -t || die "nginx config test failed: $conf"
    systemctl reload nginx >/dev/null 2>&1 || systemctl restart nginx >/dev/null || die "nginx reload/restart failed"
    ok "nginx vhost: http://$DOMAIN -> 127.0.0.1:$HOST_PORT (HTTP, pre-TLS)"
}

# Issue a Let's Encrypt cert (if missing), then rewrite the vhost to a full
# HTTP->HTTPS redirect + HTTPS proxy. Regenerating the config on every run
# keeps the script idempotent — re-running after `git pull` never loses HTTPS.
setup_tls() {
    [[ -n "$DOMAIN" ]] || return 0

    if ! certbot certificates 2>/dev/null | grep -q "Domains:.*${DOMAIN}"; then
        warn "ensure $DOMAIN resolves (A record) to this server's public IP before TLS provisioning"
        info "issuing Let's Encrypt certificate for $DOMAIN (ECDSA key)..."
        # --key-type ecdsa matches this server's ECDSA-only ssl_ciphers.
        if ! certbot certonly --webroot -w /var/www/_acme_challenge -d "$DOMAIN" \
            --non-interactive --agree-tos --register-unsafely-without-email \
            --keep-until-expiring --key-type ecdsa; then
            warn "certbot failed — is DNS for $DOMAIN pointing here yet? Re-run: sudo $0 --domain $DOMAIN"
            return 0
        fi
    fi

    local conf="/etc/nginx/sites-available/${DOMAIN}.conf"
    cat > "$conf" <<EOF
# gemini-web2api reverse proxy for $DOMAIN (generated by deploy.sh)
# The app listens on 127.0.0.1:$HOST_PORT only — never expose it directly.

# HTTP -> HTTPS (keeps the ACME webroot reachable for renewals).
server {
    listen 80;
    listen [::]:80;
    server_name $DOMAIN;

    location /.well-known/acme-challenge/ {
        root /var/www/_acme_challenge;
        default_type text/plain;
        try_files \$uri =404;
    }

    return 301 https://\$host\$request_uri;
}

# HTTPS.
server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;
    server_name $DOMAIN;

    ssl_certificate     /etc/letsencrypt/live/$DOMAIN/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/$DOMAIN/privkey.pem;
    ssl_trusted_certificate /etc/letsencrypt/live/$DOMAIN/chain.pem;

    location / {
        proxy_pass http://127.0.0.1:$HOST_PORT;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        # SSE streaming (chat completions) must not be buffered.
        proxy_buffering off;
        proxy_cache off;
        proxy_read_timeout 300s;
        proxy_send_timeout 300s;
    }
}
EOF
    nginx -t || die "nginx config test failed: $conf"
    systemctl reload nginx >/dev/null 2>&1 || systemctl restart nginx >/dev/null || die "nginx reload/restart failed"
    ok "TLS: https://$DOMAIN -> 127.0.0.1:$HOST_PORT (auto-renew via certbot)"
}

open_firewall() {
    command -v ufw >/dev/null 2>&1 || return 0
    # Never lock ourselves out of SSH: allow the real sshd port (from sshd -T),
    # and only open 80/443 when a public domain is requested.
    local ssh_port="22"
    ssh_port="$(sshd -T 2>/dev/null | awk '/^port /{print $2; exit}' || true)"
    [[ -n "$ssh_port" ]] || ssh_port="22"
    if ufw status | grep -q "Status: active"; then
        ufw allow "${ssh_port}/tcp" >/dev/null 2>&1 || true
        if [[ -n "$DOMAIN" ]]; then
            ufw allow 80/tcp >/dev/null 2>&1 || true
            ufw allow 443/tcp >/dev/null 2>&1 || true
            ok "ufw: allowed SSH ($ssh_port), 80, 443"
        else
            ok "ufw: allowed SSH ($ssh_port); public ports left closed"
        fi
    else
        info "ufw is not active — enabling with SSH ($ssh_port) only..."
        ufw allow "${ssh_port}/tcp" >/dev/null 2>&1 || true
        if [[ -n "$DOMAIN" ]]; then
            ufw allow 80/tcp >/dev/null 2>&1 || true
            ufw allow 443/tcp >/dev/null 2>&1 || true
        fi
        ufw --force enable >/dev/null || warn "could not enable ufw (ignored)"
        ok "ufw enabled"
    fi
}

smoke_test() {
    sleep 2
    local base="http://127.0.0.1:$HOST_PORT"
    local code
    code="$(curl -s -o /dev/null -w '%{http_code}' "$base/" || true)"
    ok "smoke test: GET $base/ -> HTTP $code"
    if [[ "$code" != "200" ]]; then
        warn "expected 200 — check logs: docker logs $APP_NAME"
    fi
}

# ── main ──────────────────────────────────────────────────────────────────────
main() {
    info "gemini-web2api VPS deployment"
    echo "  app dir: $APP_DIR"
    echo "  domain:  ${DOMAIN:-none (local only)}"
    echo "  port:    $HOST_PORT"
    echo

    install_docker
    prepare_config
    ensure_auth
    build_and_run
    wait_healthy
    open_firewall
    setup_nginx
    setup_tls
    smoke_test

    echo
    ok "deployed!"
    echo
    echo "  API base:      http://127.0.0.1:$HOST_PORT/v1"
    [[ -n "$DOMAIN" ]] && echo "  Public URL:    https://$DOMAIN/v1"
    echo "  Environment:   $APP_DIR/.env (single source of truth)"
    echo "  Logs:          docker logs -f $APP_NAME"
    echo "  Update:        cd $(cd "$SCRIPT_DIR" && pwd) && git pull && sudo ./deploy.sh [--domain $DOMAIN]"
    echo
    echo "  Example client:"
    echo "    curl https://${DOMAIN:-127.0.0.1:$HOST_PORT}/v1/chat/completions \\"
    echo "      -H 'Content-Type: application/json' \\"
    echo "      -H 'Authorization: Bearer \$(grep ^GEMINI_API_KEY= $APP_DIR/.env | cut -d= -f2)' \\"
    echo "      -d '{\"model\":\"gemini-3.5-flash\",\"messages\":[{\"role\":\"user\",\"content\":\"Hello!\"}]}'"
}

main
