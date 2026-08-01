#!/usr/bin/env bash
#
# agent-init.sh — generate pi, Hermes and OpenCode configs from one .env.
#
# Reads gemini-web2api settings from .env (real environment variables take
# precedence, matching the server's own precedence) and writes:
#
#   ~/.pi/agent/models.json            pi provider + models
#   ~/.pi/agent/settings.json          pi default provider/model
#   ~/.hermes/config.yaml              Hermes custom provider
#   ~/.config/opencode/opencode.json   OpenCode provider (merged with jq)
#
# Config edits are additive: existing providers, MCP servers, agent settings
# and personalities are preserved. Every existing file is first backed up to
# <file>-YYYYMMDDHHMMSS.bak. OpenCode merging needs jq (Homebrew: brew install
# jq; apt: apt-get install jq) — without it the OpenCode file is left alone.
#
# Usage:
#   ./agent-init.sh                     # reads ./.env, writes global configs
#   ./agent-init.sh --env /path/.env    # explicit env file
#   ./agent-init.sh --model gemini-3.5-flash-thinking
#   ./agent-init.sh --base-url https://llm.example.com/v1
#   ./agent-init.sh --project           # write pi config into ./.pi/ instead
#   ./agent-init.sh --dry-run           # print what would be written
#   ./agent-init.sh --help
#
set -euo pipefail

ENV_FILE="./.env"
MODEL=""
BASE_URL=""
PROJECT=0
DRY_RUN=0

info() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
ok()   { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

usage() {
    cat <<'EOF'
agent-init.sh — generate pi, Hermes and OpenCode configs from one .env.

Usage:
  ./agent-init.sh                     # reads ./.env, writes global configs
  ./agent-init.sh --env /path/.env    # explicit env file
  ./agent-init.sh --model gemini-3.5-flash-thinking
  ./agent-init.sh --base-url https://llm.example.com/v1
  ./agent-init.sh --project           # write pi config into ./.pi/ instead
  ./agent-init.sh --dry-run           # print what would be written
  ./agent-init.sh --help

Options:
  --env PATH      .env file to read (default ./.env)
  --model NAME    default model (default: $GEMINI_DEFAULT_MODEL or gemini-3.6-flash)
  --base-url URL  override the API base URL
  --project       write pi config into ./.pi/ (per project) instead of ~/.pi/agent/
  --dry-run       print the paths that would be written, write nothing
  --help          show this help

Existing config files are backed up to <file>-YYYYMMDDHHMMSS.bak and updated
additively (existing providers / MCP servers / agent settings are preserved).
OpenCode merging requires jq; without it the OpenCode file is left untouched.
EOF
    exit 0
}

while [[ $# -gt 0 ]]; do
    case "$1" in
        --env)     ENV_FILE="${2:-}"; shift 2 ;;
        --model)   MODEL="${2:-}"; shift 2 ;;
        --base-url) BASE_URL="${2:-}"; shift 2 ;;
        --project) PROJECT=1; shift ;;
        --dry-run) DRY_RUN=1; shift ;;
        --help|-h) usage ;;
        *) die "unknown option: $1 (see --help)" ;;
    esac
done

# json_escape S — escape a string for embedding in JSON (quotes/backslashes).
json_escape() {
    local s="$1"
    s="${s//\\/\\\\}"
    s="${s//\"/\\\"}"
    printf '%s' "$s"
}

# yaml_quote S — single-quote a string for YAML (double embedded single
# quotes). Uses sed (double-quoted) instead of ${s//'/''}, which trips the
# bash parser inside double quotes.
yaml_quote() {
    local s="$1"
    s="$(printf '%s' "$s" | sed "s/'/''/g" || true)"
    printf "'%s'" "$s"
}

# get_val KEY — real process env wins over the .env file (same precedence as
# the server: os.LookupEnv first, then dotenv map). Handles quoted values.
get_val() {
    local key="$1" v=""
    if [[ -n "${!key+x}" ]]; then
        printf '%s' "${!key}"
        return
    fi
    [[ -f "$ENV_FILE" ]] || return 0
    v="$(grep -E "^[[:space:]]*${key}=" "$ENV_FILE" | tail -1 | sed -E 's/^[[:space:]]*[A-Za-z_][A-Za-z0-9_]*=[[:space:]]*//; s/^"//; s/"$//; s/^'"'"'//; s/'"'"'$//' || true)"
    printf '%s' "$v"
}

# ── gather settings ───────────────────────────────────────────────────────────
# key_is_set KEY — true if the key is present in the real env (even set-but-
# empty) or in the .env file, matching the server's envValue semantics where
# a set-but-empty value is authoritative (empty GEMINI_API_KEY= clears keys).
key_is_set() {
    local key="$1"
    if [[ -n "${!key+x}" ]]; then
        return 0
    fi
    [[ -f "$ENV_FILE" ]] && grep -qE "^[[:space:]]*${key}=" "$ENV_FILE" && return 0
    return 1
}

API_KEY=""
if key_is_set GEMINI_API_KEY; then
    API_KEY="$(get_val GEMINI_API_KEY)"
elif key_is_set GEMINI_API_KEYS; then
    # GEMINI_API_KEYS is comma-separated; take the first key.
    API_KEY="$(get_val GEMINI_API_KEYS | cut -d, -f1 | sed -E 's/^[[:space:]]*//; s/[[:space:]]*$//')"
fi

[[ -n "$MODEL" ]] || MODEL="$(get_val GEMINI_DEFAULT_MODEL)"
[[ -n "$MODEL" ]] || MODEL="gemini-3.6-flash"

if [[ -n "$BASE_URL" ]]; then
    : # explicit override
else
    HOST="$(get_val GEMINI_HOST)"
    [[ -n "$HOST" ]] || HOST="0.0.0.0"
    # Clients cannot reach 0.0.0.0 / ::; use the loopback address.
    [[ "$HOST" == "0.0.0.0" || "$HOST" == "::" || "$HOST" == "::0" ]] && HOST="127.0.0.1"
    PORT="$(get_val GEMINI_PORT)"
    [[ -n "$PORT" ]] || PORT="8081"
    BASE_URL="http://${HOST}:${PORT}/v1"
fi

PI_DIR="$HOME/.pi/agent"
HERMES_FILE="$HOME/.hermes/config.yaml"
OPENCODE_FILE="$HOME/.config/opencode/opencode.json"
if [[ "$PROJECT" -eq 1 ]]; then
    PI_DIR="./.pi"
fi

# One timestamp per run so all backups from the same run share a suffix.
TS="$(date +%Y%m%d%H%M%S)"

have_jq() { command -v jq >/dev/null 2>&1; }

# backup_file FILE — copy to FILE-YYYYMMDDHHMMSS.bak (no-op when missing or dry run).
backup_file() {
    local f="$1"
    [[ -f "$f" ]] || return 0
    if [[ "$DRY_RUN" -eq 1 ]]; then
        info "would back up: $f -> $f-$TS.bak"
        return 0
    fi
    cp -p "$f" "$f-$TS.bak"
    info "backed up: $f-$TS.bak"
}

info "gemini-web2api -> pi, Hermes & OpenCode config generator"
echo "  env file:   $ENV_FILE"
echo "  base url:   $BASE_URL"
echo "  model:      $MODEL"
echo "  api key:    ${API_KEY:+set (${#API_KEY} chars)}${API_KEY:-none — auth disabled on the server}"
echo "  pi dir:     $PI_DIR"
echo "  hermes:     $HERMES_FILE"
echo "  opencode:   $OPENCODE_FILE"
echo

# ── pi: models.json ───────────────────────────────────────────────────────────
write_pi_models() {
    mkdir -p "$PI_DIR"
    local target="$PI_DIR/models.json"
    local base_url_esc api_key_esc tmp
    base_url_esc="$(json_escape "$BASE_URL")"
    api_key_esc="$(json_escape "${API_KEY:-sk-placeholder}")"
    tmp="$(mktemp)"
    cat > "$tmp" <<EOF
{
  "providers": {
    "gemini-web2api": {
      "baseUrl": "$base_url_esc",
      "api": "openai-completions",
      "apiKey": "$api_key_esc",
      "models": [
        {
          "id": "gemini-3.6-flash",
          "name": "Gemini Flash (default)",
          "input": ["text"],
          "contextWindow": 128000,
          "maxTokens": 8192
        },
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
        },
        {
          "id": "gemini-3.5-flash-thinking-lite",
          "name": "Gemini Thinking Lite",
          "input": ["text"],
          "contextWindow": 160000,
          "maxTokens": 15000
        },
        {
          "id": "gemini-3.1-pro",
          "name": "Gemini Pro",
          "input": ["text"],
          "contextWindow": 128000,
          "maxTokens": 8192
        },
        {
          "id": "gemini-3.1-pro-enhanced",
          "name": "Gemini Pro Enhanced",
          "input": ["text"],
          "contextWindow": 128000,
          "maxTokens": 8192
        },
        {
          "id": "gemini-auto",
          "name": "Gemini Auto",
          "input": ["text"],
          "contextWindow": 128000,
          "maxTokens": 8192
        },
        {
          "id": "gemini-flash-lite",
          "name": "Gemini Flash Lite",
          "input": ["text"],
          "contextWindow": 128000,
          "maxTokens": 8192
        }
      ]
    }
  }
}
EOF
    if [[ -f "$target" ]] && have_jq; then
        backup_file "$target"
        jq --argjson add "$(cat "$tmp")" \
           '.providers = ((.providers // {}) + $add.providers)' "$target" > "$tmp.merged" \
            || die "jq merge failed for $target"
        mv "$tmp.merged" "$target"
        ok "merged provider into $target (jq, existing providers kept)"
    else
        backup_file "$target"
        if [[ -f "$target" ]]; then
            warn "jq not found — replacing $target (existing providers would be lost; install jq to merge)"
        fi
        mv "$tmp" "$target"
        ok "wrote $target"
    fi
    rm -f "$tmp"
}

# ── pi: settings.json ─────────────────────────────────────────────────────────
write_pi_settings() {
    local target="$PI_DIR/settings.json"
    local model_esc
    model_esc="$(json_escape "$MODEL")"
    mkdir -p "$PI_DIR"
    if [[ -f "$target" ]] && have_jq; then
        backup_file "$target"
        jq --arg p "gemini-web2api" --arg m "$MODEL" \
           '.defaultProvider = $p | .defaultModel = $m' "$target" > "$target.tmp" \
            || die "jq merge failed for $target"
        mv "$target.tmp" "$target"
        ok "updated $target (defaultProvider/model via jq, other keys kept)"
    else
        backup_file "$target"
        if [[ -f "$target" ]]; then
            warn "jq not found — replacing $target (existing settings would be lost; install jq to merge)"
        fi
        cat > "$target" <<EOF
{
  "defaultProvider": "gemini-web2api",
  "defaultModel": "$model_esc"
}
EOF
        ok "wrote $target"
    fi
}

# ── Hermes: config.yaml ───────────────────────────────────────────────────────
# Replaces only the top-level `model:` block; everything else (fallback
# providers, toolsets, agent settings, personalities) is preserved as-is.
write_hermes() {
    local target="$HERMES_FILE"
    mkdir -p "$(dirname "$target")"
    local model_q base_url_q api_key_q block blockfile
    model_q="$(yaml_quote "$MODEL")"
    base_url_q="$(yaml_quote "$BASE_URL")"
    api_key_q="$(yaml_quote "${API_KEY:-no-key-required}")"
    block="model:
  provider: \"custom\"
  default: $model_q
  base_url: $base_url_q
  api_key: $api_key_q
"
    blockfile="$(mktemp)"
    printf '%s' "$block" > "$blockfile"
    if [[ -f "$target" ]]; then
        backup_file "$target"
        if grep -q '^model:' "$target"; then
            # Replace the existing top-level `model:` block, keep the rest.
            awk -v bf="$blockfile" '
                BEGIN { replaced=0; inskip=0 }
                !replaced && /^model:/ {
                    while ((getline line < bf) > 0) print line
                    close(bf)
                    replaced=1
                    inskip=1
                    next
                }
                inskip {
                    # skip lines until the next top-level key (col 0) / comment
                    if ($0 ~ /^[^[:space:]#]/ && $0 != "") { inskip=0 } else { next }
                }
                { print }
            ' "$target" > "$target.tmp" || die "merge failed for $target"
            mv "$target.tmp" "$target"
            ok "updated $target (model block replaced, rest preserved)"
        else
            # no `model:` key — prepend the block
            cat "$blockfile" "$target" > "$target.tmp"
            mv "$target.tmp" "$target"
            ok "updated $target (model block prepended)"
        fi
    else
        cp "$blockfile" "$target"
        ok "wrote $target"
    fi
    rm -f "$blockfile"
}

# ── OpenCode: opencode.json ───────────────────────────────────────────────────
# Adds the gemini-web2api provider into the existing provider map (jq merge),
# keeping every other provider and the mcp section intact.
write_opencode() {
    local target="$OPENCODE_FILE"
    mkdir -p "$(dirname "$target")"
    local tmp base_url_esc api_key_esc
    base_url_esc="$(json_escape "$BASE_URL")"
    api_key_esc="$(json_escape "${API_KEY:-sk-placeholder}")"
    tmp="$(mktemp)"
    cat > "$tmp" <<EOF
{
  "provider": {
    "gemini-web2api": {
      "npm": "@ai-sdk/openai-compatible",
      "name": "Gemini via gemini-web2api",
      "options": {
        "baseURL": "$base_url_esc",
        "apiKey": "$api_key_esc"
      },
      "models": {
        "gemini-3.6-flash": { "name": "Gemini Flash (default)" },
        "gemini-3.5-flash": { "name": "Gemini Flash" },
        "gemini-3.5-flash-thinking": { "name": "Gemini Thinking" },
        "gemini-3.5-flash-thinking-lite": { "name": "Gemini Thinking Lite" },
        "gemini-3.1-pro": { "name": "Gemini Pro" },
        "gemini-3.1-pro-enhanced": { "name": "Gemini Pro Enhanced" },
        "gemini-auto": { "name": "Gemini Auto" },
        "gemini-flash-lite": { "name": "Gemini Flash Lite" }
      }
    }
  }
}
EOF
    if [[ -f "$target" ]] && have_jq; then
        backup_file "$target"
        jq --argjson add "$(cat "$tmp")" \
           '.provider = ((.provider // {}) + $add.provider)' "$target" > "$target.tmp" \
            || die "jq merge failed for $target"
        mv "$target.tmp" "$target"
        ok "merged OpenCode provider into $target (jq, existing providers/MCP kept)"
    elif [[ -f "$target" ]]; then
        warn "jq not found — OpenCode config left untouched (install jq to merge the provider)"
    else
        mv "$tmp" "$target"
        ok "wrote $target"
    fi
    rm -f "$tmp"
}

if [[ "$DRY_RUN" -eq 1 ]]; then
    info "dry run — files that would be written (each existing one first backed up to -$TS.bak):"
    echo "  $PI_DIR/models.json"
    echo "  $PI_DIR/settings.json"
    echo "  $HERMES_FILE"
    echo "  $OPENCODE_FILE"
    exit 0
fi

write_pi_models
write_pi_settings
write_hermes
write_opencode

echo
ok "done! Run 'pi', 'hermes' or 'opencode' in a terminal to start."
if [[ -z "$API_KEY" ]]; then
    warn "no API key found — server auth is disabled; set GEMINI_API_KEY in $ENV_FILE for production."
fi
