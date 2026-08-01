# gemini-web2api (Go)

![Cover](cover-readme.jpg)

Convert Google Gemini's web interface into an OpenAI-compatible API. Zero cost,
cross-platform. This is a Go reimplementation of the
[gemini-web2api](https://github.com/Sophomoresty/gemini-web2api) Python project with
the same endpoints and behavior — no third-party dependencies, only the Go
standard library.

## Features

- **Optional API Keys**: no auth when `api_keys` is empty, OpenAI-style Bearer auth when configured
- **OpenAI Compatible**: drop-in replacement for `/v1/chat/completions` and `/v1/models`
- **Tool Calling**: full function calling support (OpenAI format)
- **Multiple Models**: Flash (3.6), Extended Thinking (20k+ char output), Pro, Auto, Lite
- **Thinking Depth**: adjustable via `@think=N` suffix (0=deepest, 4=shallowest)
- **Web Search**: built-in internet access (Gemini's native search)
- **Streaming**: SSE streaming support
- **Codex CLI**: Responses API (`/v1/responses`) for OpenAI Codex integration
- **Gemini CLI**: Google native API (`/v1beta/models`) for Gemini CLI compatibility
- **Embeddings**: `/v1/embeddings` via the official Google AI Studio API (not in the Python original)

## Quick Start

```bash
go build -o gemini-web2api ./src
./gemini-web2api
```

Or run without building:

```bash
go run ./src
```

Server starts at `http://localhost:8081/v1`.

## Client Configuration

### Cherry Studio / ChatBox / any OpenAI client

| Field | Value |
|-------|-------|
| Base URL | `http://localhost:8081/v1` |
| API Key | any `GEMINI_API_KEY`/`GEMINI_API_KEYS` value from `.env`; anything if not configured |
| Model | `gemini-3.5-flash-thinking` |

### curl

```bash
curl http://localhost:8081/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer sk-your-key" \
  -d '{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"Hello!"}]}'
```

### OpenAI Python SDK

```python
from openai import OpenAI
client = OpenAI(base_url="http://localhost:8081/v1", api_key="sk-your-key")
resp = client.chat.completions.create(
    model="gemini-3.5-flash-thinking",
    messages=[{"role": "user", "content": "Explain quantum computing"}]
)
print(resp.choices[0].message.content)
```

### Gemini CLI

```bash
export GEMINI_API_KEY=none
export GOOGLE_GEMINI_BASE_URL=http://localhost:8081
gemini
```

Supports Google native API endpoints:

- `GET /v1beta/models` — list models
- `POST /v1beta/models/{model}:generateContent` — non-streaming
- `POST /v1beta/models/{model}:streamGenerateContent` — streaming (SSE)

### Embeddings

`POST /v1/embeddings` is OpenAI-compatible (this endpoint does **not** exist in
 the Python original). The Gemini web session cannot produce embeddings, so it
 proxies to the official Google AI Studio API and needs a separate key:

```bash
export GEMINI_EMBEDDINGS_API_KEY=AIza...   # https://aistudio.google.com/apikey
./gemini-web2api
```

```bash
curl http://localhost:8081/v1/embeddings \
  -H 'Content-Type: application/json' -H 'Authorization: Bearer sk-gemini' \
  -d '{"model": "text-embedding-3-small", "input": ["Hello world", "Foo bar"]}'
```

Supported request fields: `model`, `input` (string or array of strings),
`encoding_format` (`float` or `base64`), `dimensions` (mapped to Gemini
`outputDimensionality`). OpenAI-style model names (`text-embedding-3-small`,
`text-embedding-ada-002`, ...) are routed to `GEMINI_EMBEDDINGS_MODEL`
(default `text-embedding-004`); native Gemini embedding names pass through.

The response follows the OpenAI format (`object: "list"`, `data[].embedding`,
`usage.prompt_tokens`). Token-array inputs are not supported.

## Local AI agents

The bridge is a drop-in OpenAI-compatible endpoint, so any agent that accepts a
custom `base_url` can use it. All examples assume the server runs on
`http://localhost:8081/v1` and that `GEMINI_API_KEY` is exported (if you run
without keys, use any placeholder). The examples use `gemini-3.5-flash`;
`gemini-3.6-flash` is the server's default model.

### One-shot setup: agent-init.sh

Instead of writing the configs below by hand, generate the **pi**, **Hermes**
and **OpenCode** configs from your existing `.env`:

```bash
./agent-init.sh                          # reads ./.env, writes global configs
./agent-init.sh --env /path/.env --model gemini-3.5-flash-thinking
./agent-init.sh --base-url https://llm.example.com/v1   # point at a public server
./agent-init.sh --project                # pi config into ./.pi/ (per project)
./agent-init.sh --dry-run                # preview paths before writing
```

It reads `GEMINI_API_KEY`/`GEMINI_API_KEYS`, `GEMINI_HOST`, `GEMINI_PORT` and
`GEMINI_DEFAULT_MODEL` (real env vars win over the file, same precedence as
the server) and writes:

- `~/.pi/agent/models.json` + `~/.pi/agent/settings.json`
- `~/.hermes/config.yaml`
- `~/.config/opencode/opencode.json` (requires `jq`; merged into the existing
  provider map so your other providers and MCP servers are kept)

Every existing config file is first **backed up** to `<file>-YYYYMMDDHHMMSS.bak`
and then updated **additively** — the Hermes `model:` block is replaced while
agent settings/personalities are preserved, and pi/OpenCode only gain a new
provider (existing defaults are kept for OpenCode; pi's `defaultProvider`/
`defaultModel` are pointed at `gemini-web2api`).

### OpenCode

Create `opencode.json` in your project (or merge into the existing one):

```json
{
  "$schema": "https://opencode.ai/config.json",
  "provider": {
    "gemini-web2api": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Gemini via gemini-web2api",
      "options": {
        "baseURL": "http://localhost:8081/v1",
        "apiKey": "{env:GEMINI_API_KEY}"
      },
      "models": {
        "gemini-3.5-flash": {
          "name": "Gemini Flash"
        },
        "gemini-3.5-flash-thinking": {
          "name": "Gemini Thinking"
        }
      }
    }
  },
  "model": "gemini-web2api/gemini-3.5-flash"
}
```

### HermesAgent

Edit `~/.hermes/config.yaml` (secrets can go to `~/.hermes/.env`):

```yaml
model:
  provider: "custom"
  default: "gemini-3.5-flash"
  base_url: "http://localhost:8081/v1"
  api_key: "${GEMINI_API_KEY}"   # or "no-key-required" when auth is disabled
```

### pi

Edit `~/.pi/agent/models.json` (or `.pi/models.json` per project):

```json
{
  "providers": {
    "gemini-web2api": {
      "baseUrl": "http://localhost:8081/v1",
      "api": "openai-completions",
      "apiKey": "$GEMINI_API_KEY",
      "models": [
        {
          "id": "gemini-3.5-flash",
          "name": "Gemini Flash",
          "input": ["text"],
          "contextWindow": 128000,
          "maxTokens": 8192
        },
        {
          "id": "gemini-3.5-flash-thinking",
          "name": "Gemini Thinking",
          "input": ["text"],
          "contextWindow": 200000,
          "maxTokens": 20000
        }
      ]
    }
  }
}
```

Make it the default in `~/.pi/agent/settings.json` (or `.pi/settings.json`):

```json
{
  "defaultProvider": "gemini-web2api",
  "defaultModel": "gemini-3.5-flash"
}
```

## Available Models

| Model | Description | Output |
|-------|-------------|--------|
| `gemini-3.6-flash` | All-around model (latest) | ~12k chars |
| `gemini-3.5-flash` | Alias for gemini-3.6-flash | ~12k chars |
| `gemini-3.5-flash-thinking` | Extended thinking, longest output | **~20k chars** |
| `gemini-3.5-flash-thinking-lite` | Adaptive thinking depth | ~15k chars |
| `gemini-3.1-pro` | Advanced math & code (needs cookie) | ~12k chars |
| `gemini-auto` | Auto model selection | varies |
| `gemini-flash-lite` | Fastest answers, lightweight | ~10k chars |

### Thinking Depth

Append `@think=N` to any model name:

```
gemini-3.5-flash-thinking@think=0   # deepest (default)
gemini-3.5-flash-thinking@think=2   # medium
gemini-3.5-flash-thinking@think=4   # shallowest
```

## Optional: Cookie for Pro

Anonymous access works for all models, but `gemini-3.1-pro` routes to Flash
without authentication. To get real Pro routing, you need a **Gemini Advanced
(paid subscription)** account cookie:

```bash
./gemini-web2api --cookie-file cookie.txt
```

### How to get cookies

1. Open Chrome, go to [gemini.google.com](https://gemini.google.com) and sign in with a **Gemini Advanced** Google account
2. Open DevTools (F12) → Application → Cookies → `https://gemini.google.com`
3. Copy these cookie values: `SID`, `HSID`, `SSID`, `APISID`, `SAPISID`, `__Secure-1PSID`
4. Create `cookie.txt` in this format:

```
SID=your_sid_value; HSID=your_hsid_value; SSID=your_ssid_value; APISID=your_apisid_value; SAPISID=your_sapisid_value; __Secure-1PSID=your_1psid_value
```

Or use the JSON format:

```json
{"cookie": "SID=xxx; HSID=xxx; ...", "sapisid": "your_sapisid_value"}
```

### Authenticated account path and XSRF token

If the signed-in Gemini page URL contains an account index, such as
`https://gemini.google.com/u/1/app/...`, set `GEMINI_AUTH_USER` to that index.
Authenticated web requests may also require the page XSRF token, exposed as
`SNlM0e` in the page source; pass it as `GEMINI_XSRF_TOKEN` in `.env` (sent as
the `at` form field).

```bash
GEMINI_COOKIE_FILE=/app/cookie.txt
GEMINI_AUTH_USER=1
GEMINI_XSRF_TOKEN=AOOh0P...
GEMINI_BL=boq_assistant-bard-web-server_YYYYMMDD.xx_p0
```

If authenticated requests return HTTP 400 with an `xsrf` error, refresh Gemini
Web, update `GEMINI_XSRF_TOKEN`, and make sure `GEMINI_AUTH_USER` matches the
`/u/<index>/` part of the browser URL.

Pro routing requires **Gemini Advanced** (paid subscription). A free Google
account cookie will authenticate but silently fall back to Flash.

## Configuration

**`.env` is the single source of truth.** Precedence: **CLI flags > real env
vars > `.env` file > built-in defaults**. `config.json` support was removed.

Create a `.env` file in the working directory (or pass `--env /path/to/.env`):

```bash
cp .env.example .env
```

When `GEMINI_API_KEYS` is empty, authentication is disabled. When one or more
keys are set, `/v1/*` endpoints require `Authorization: Bearer <key>`,
`x-api-key: <key>`, `x-goog-api-key: <key>`, or `?key=<key>`.

| Variable | Description |
|----------|-------------|
| `GEMINI_PORT` | Listen port |
| `GEMINI_HOST` | Bind host |
| `GEMINI_RETRY_ATTEMPTS` | Upstream retry count |
| `GEMINI_RETRY_DELAY_SEC` | Delay between retries (seconds) |
| `GEMINI_REQUEST_TIMEOUT_SEC` | Upstream request timeout (seconds) |
| `GEMINI_BL` | Gemini backend version string (`boq_assistant-bard-web-server_...`) |
| `GEMINI_AUTH_USER` | Account index for `/u/<index>/` URLs |
| `GEMINI_XSRF_TOKEN` | Page XSRF token (`SNlM0e`) sent as the `at` field |
| `GEMINI_DEFAULT_MODEL` | Default model name |
| `GEMINI_LOG_REQUESTS` | `true`/`false` — enable request logging |
| `GEMINI_COOKIE_FILE` | Path to the cookie file |
| `GEMINI_PROXY` | HTTP proxy URL, e.g. `http://127.0.0.1:7890` |
| `GEMINI_API_KEY` | Single API key (takes precedence over `GEMINI_API_KEYS`) |
| `GEMINI_API_KEYS` | Comma-separated API keys; empty disables auth |
| `GEMINI_REQUIRE_AUTH` | `true`/`false` — refuse to start when no keys are configured |
| `GEMINI_EMBEDDINGS_API_KEY` | Official Google AI Studio API key for `/v1/embeddings` |
| `GEMINI_EMBEDDINGS_MODEL` | Gemini embedding model, default `text-embedding-004` |
| `GEMINI_WEB_BASE` | Gemini web upstream base URL (default `https://gemini.google.com`; for tests/mocks) |
| `GEMINI_EMBEDDINGS_API_BASE` | Google AI Studio API base URL (default `https://generativelanguage.googleapis.com`; for tests/mocks) |
| `GEMINI_MAX_TOOL_DESC` | Max runes per tool description in the prompt, default `150`. Clients that send many tools (hermes sends 40+) can push the prompt past Gemini Web's anonymous size limit (~120KB), which upstream rejects with a silent `BardErrorInfo [1152]` → `content:null`. Truncating keeps large tool sets working. `0` = unlimited. |
| `GEMINI_DUMP_RAW` | `true`/`false` — dump the raw upstream response to `/tmp/gemini-raw-empty.txt` when a 200 arrives with no extractable text (diagnostics). |

Note: `GEMINI_HOST`, `GEMINI_BL` and `GEMINI_DEFAULT_MODEL` ignore empty
values (they cannot be unset via env), while an empty `GEMINI_API_KEYS=`
explicitly clears the key list and disables authentication.

Real process environment variables take precedence over the `.env` file, so
Docker/CI secrets work without a `.env` file:

```bash
export GEMINI_API_KEYS=sk-secret
export GEMINI_PORT=9000
./gemini-web2api
```

The server prints the loaded env file path on startup:

```
gemini-web2api v1.1.0
  Env file:  .env
  Listening: http://0.0.0.0:9000
  Base URL:  http://localhost:9000/v1
  Auth:      enabled (1 key(s))
  Embeddings: enabled (text-embedding-004)
```

If no keys are configured the banner warns explicitly:
`Auth: DISABLED — API is open to everyone!`

## Exposing to the Internet

Before putting the endpoint on a public network, at minimum:

1. **Set an API key and require it** — never run open:

   ```bash
   GEMINI_API_KEY=sk-very-secret
   GEMINI_REQUIRE_AUTH=true
   ```

   With `GEMINI_REQUIRE_AUTH=true` the server **refuses to start** when no
   keys are configured, so a misconfigured deployment fails closed instead of
   exposing an open API.

2. **Terminate TLS in front of the server** (Caddy, Nginx, or Cloudflare
   Tunnel). The server itself speaks plain HTTP.

3. **Rate-limit at the proxy layer** to protect your Google quota — sustained
   abuse may get you throttled by Gemini upstream.

4. **Check the startup banner** — it prints the auth state
   (`enabled (1 key(s))` or `DISABLED — API is open to everyone!`).

## Docker

The image is built in **two stages** with security hardening baked in:

- Final stage is `scratch` — no shell, no package manager, no tools (smallest attack surface)
- Static binary (`CGO_ENABLED=0`, `-trimpath`, stripped)
- Runs as the unprivileged `nobody` user (UID/GID 65534)
- CA certificates included for HTTPS to `gemini.google.com`
- `HEALTHCHECK` (probes `GET /` via the binary itself — works without a shell)
- **Fails closed by default**: the baked `.env` forces `GEMINI_REQUIRE_AUTH=true`,
  so the container refuses to start without an API key. Override explicitly
  with `-e GEMINI_REQUIRE_AUTH=false` (or set keys via `--env-file`) if you
  know what you're doing.

```bash
cp .env.example .env
docker build -t gemini-web2api .

docker run -d --name gemini-web2api -p 8081:8081 \
  --read-only \
  --tmpfs /tmp \
  --cap-drop=ALL \
  --security-opt no-new-privileges \
  --env-file .env \
  gemini-web2api
```

Runtime hardening flags: read-only rootfs, no capabilities, no new privileges.
The image itself already enforces non-root + no shell, so these flags are the
defense-in-depth layer.

Or use Docker Compose (hardening included in `docker-compose.local.yml`):

```bash
cp .env.example .env
docker compose up -d
```

Pass configuration through the environment instead of a mounted config file:

```bash
docker run -d --name gemini-web2api -p 8081:8081 \
  --env-file .env gemini-web2api
```

Environment variables set this way work exactly like a local `.env` file.

To mount a cookie file:

```bash
docker run -d --name gemini-web2api -p 8081:8081 \
  --env-file .env -v ./cookie.txt:/app/cookie.txt gemini-web2api
```

Set `GEMINI_COOKIE_FILE=/app/cookie.txt` in `.env`.

> **Note**: If you get empty responses (`content: null`), the server now
> surfaces the upstream rejection as a 502 with the actual `BardErrorInfo`
> code instead of a silent empty reply. With Docker's default bridge network
> some NAT IP ranges are also rejected by Gemini — switch to host networking
> (`--network host` or `network_mode: host`) if the 502 persists for small
> prompts.

## Deploying on a VPS

The included [`deploy.sh`](deploy.sh) automates the whole process on a fresh
Ubuntu/Debian VPS:

```bash
# on the VPS, as root
sudo ./deploy.sh                            # local-only, 127.0.0.1:8081
sudo ./deploy.sh --domain api.example.com   # + nginx vhost + TLS (certbot)
```

What it does:

1. Installs Docker from the official repo if missing
2. Creates `/opt/gemini-web2api/` with `.env` (preserved across re-runs —
   your secrets live there, not in the repo)
3. Asks for a `GEMINI_API_KEY` and sets `GEMINI_REQUIRE_AUTH=true`, so the
   server refuses to start without keys (fail closed)
4. Builds the hardened two-stage image
5. Runs the container with `--read-only`, `--cap-drop=ALL`,
   `--no-new-privileges` and `--restart unless-stopped`, bound to
   `127.0.0.1:8081` only
6. With `--domain`, creates an **nginx** vhost (in `sites-available` + symlink
   into `sites-enabled`), issues a Let's Encrypt certificate via **certbot**
   (ECDSA key, `--key-type ecdsa`) and opens ports 80/443 in ufw
7. Waits for the health check and runs a smoke test

The container deliberately listens on `127.0.0.1` — it is never exposed
directly to the internet. nginx (or your own reverse proxy / Cloudflare
Tunnel) is the only public entry point. Existing nginx setups on the server
are left untouched; the script only adds one vhost per `--domain`.

### Get the code onto the VPS

```bash
# from your machine: copy the project (no secrets — they live in /opt)
git clone https://github.com/you/gemini-web2api.git && cd gemini-web2api
# or: rsync -avz --exclude .git --exclude .env ./ user@vps:/opt/src/gemini-web2api/
```

### Manual equivalent (no script)

```bash
# 1. build & run
cp .env.example .env                 # edit: GEMINI_API_KEY, GEMINI_REQUIRE_AUTH=true
sudo docker build -t gemini-web2api .
sudo docker run -d --name gemini-web2api -p 127.0.0.1:8081:8081 \
  --read-only --tmpfs /tmp --cap-drop=ALL --security-opt no-new-privileges \
  --restart unless-stopped --env-file .env \
  gemini-web2api

# 2. reverse proxy with TLS (nginx + certbot)
sudo apt-get install -y nginx certbot python3-certbot-nginx
cat > /etc/nginx/sites-available/api.example.com.conf <<'EOF'
server {
    listen 80;
    listen [::]:80;
    server_name api.example.com;

    location /.well-known/acme-challenge/ {
        root /var/www/_acme_challenge;
        default_type text/plain;
        try_files $uri =404;
    }

    location / {
        proxy_pass http://127.0.0.1:8081;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_buffering off;      # SSE streaming must not be buffered
        proxy_cache off;
        proxy_read_timeout 300s;
        proxy_send_timeout 300s;
    }
}
EOF
sudo ln -sf /etc/nginx/sites-available/api.example.com.conf /etc/nginx/sites-enabled/
sudo nginx -t && sudo systemctl reload nginx
sudo certbot --nginx -d api.example.com --redirect --key-type ecdsa
```

### Firewall

If you use ufw, only expose what the proxy needs:

```bash
sudo ufw allow OpenSSH
sudo ufw allow 80,443/tcp
sudo ufw enable
```

Do **not** open 8081 publicly — the app stays on 127.0.0.1 behind the proxy.

### No public IP / no domain: Cloudflare Tunnel

The container already binds to `127.0.0.1:8081`, which is exactly what
`cloudflared` expects. (Or skip the tunnel entirely and point the A record
at the server: `deploy.sh --domain llm.example.com` + nginx does TLS for you.) No ports, no TLS config, no public IP needed:

```bash
cloudflared tunnel login
cloudflared tunnel create gemini
cloudflared tunnel route dns gemini api.example.com
# config.yml
#   tunnel: <tunnel-id>
#   credentials-file: /root/.cloudflared/<tunnel-id>.json
#   ingress:
#     - hostname: api.example.com
#       service: http://localhost:8081
#     - service: http_status:404
cloudflared tunnel run gemini
```

### Updating

```bash
cd <repo> && git pull && sudo ./deploy.sh [--domain api.example.com]
```

Config and `.env` in `/opt/gemini-web2api/` are preserved; only the image and
container are rebuilt.

## Proxy

If you cannot access `gemini.google.com` directly (connection timeout):

**Method 1: CLI argument**

```bash
./gemini-web2api --proxy http://127.0.0.1:7890
```

**Method 2: .env**

```bash
GEMINI_PROXY=http://127.0.0.1:7890
```

**Method 3: Environment variable** (auto-detected)

```bash
export HTTPS_PROXY=http://127.0.0.1:7890
./gemini-web2api
```

Works with Clash, V2Ray, Shadowsocks, or any HTTP proxy.

## Tool Calling

```python
resp = client.chat.completions.create(
    model="gemini-3.5-flash",
    messages=[{"role": "user", "content": "What's the weather in Tokyo?"}],
    tools=[{
        "type": "function",
        "function": {
            "name": "get_weather",
            "description": "Get weather for a city",
            "parameters": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}
        }
    }]
)
```

## CLI Arguments

```
--port 8081              listen port (overrides env)
--env path               path to .env file (defaults to ./.env)
--cookie-file path       path to cookie file (overrides env)
--proxy http://host:port HTTP proxy (overrides env)
--require-auth           refuse to start when no API keys are configured
--check                  health check: GET / on the configured port, exit 0/1
                         (used by the Docker HEALTHCHECK)
--version                print version and exit
```

## Limitations

- **No image/multimodal input**: Gemini's image upload requires a proprietary streaming RPC protocol (WIZ/ProcessFile). Image inputs in messages are ignored with a note.
- **Not real Pro/Ultra**: Without a paid subscription cookie, `gemini-3.1-pro` routes to the same Flash model.
- **Single-turn only**: Each request is an independent conversation. Multi-turn context is simulated by including previous messages in the prompt.
- **Rate limits**: Google may throttle high-frequency requests. The server retries automatically but sustained heavy use may be blocked.

## Development

```bash
go build -o gemini-web2api ./src   # build
go vet ./src/...                   # static checks
go test ./src/...                  # unit + integration tests
```

### Testing

- **Unit tests** — `go test ./src/...`: parser/handler/payload logic, no network.
- **Integration tests** — also `go test ./src/...`: spin up the real HTTP
  server (`httptest.NewServer`) against a **mock Gemini upstream** (a local
  server that answers with synthetic `wrb.fr` lines) and drive the full
  request → auth → handler → upstream → response pipeline over real HTTP:
  chat completions (plain, streaming, tool calls), `/v1/responses`, Google
  native endpoints, embeddings, auth, CORS, error propagation (502 on
  upstream failure). No real Gemini calls happen.
- **E2E tests** — `go test -tags e2e ./e2e/...`: build the real binary with
  `go build`, run it as a subprocess with a temporary `.env` (random port),
  and exercise the API over real HTTP. The binary is pointed at a local mock
  upstream via `GEMINI_WEB_BASE` / `GEMINI_EMBEDDINGS_API_BASE` (env vars
  that default to the real Google URLs). Also covers fail-closed startup
  (`GEMINI_REQUIRE_AUTH=true` without keys → refuses to start), `--check`
  health probing, and real-env-over-`.env` precedence.

The `GEMINI_WEB_BASE` / `GEMINI_EMBEDDINGS_API_BASE` knobs are meant for
this test setup — in production they default to the real Google endpoints
and should be left unset.

## How It Works

This tool reverse-engineers Google Gemini's web StreamGenerate protocol. It
sends requests to the same endpoint that the Gemini web app uses, converting
between OpenAI's API format and Gemini's internal protobuf-like format.

The model selection is controlled by field `[79]` in the request payload, mapped
from Gemini's frontend JavaScript source (`MODE_CATEGORY` enum).

## License

MIT
