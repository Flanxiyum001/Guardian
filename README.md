# 🛡️ AIGuard

A privacy gateway for LLM traffic. A lightweight **Go API gateway** sits in front
of any OpenAI-compatible endpoint, detects PII with **Microsoft Presidio**, swaps
each finding for an opaque token, and keeps the real value in **Redis**. A
**Next.js dashboard** shows the live counters.

```
            ┌─────────────┐   sanitized text   ┌────────────────────┐
 client ───▶│ Go gateway  │───────────────────▶│  Upstream LLM API  │
            │   :8080     │                    └────────────────────┘
            └──────┬──────┘
              ┌────┴─────┐
              ▼          ▼
     ┌──────────────┐  ┌───────────────┐
     │ Presidio     │  │ Redis         │  tokens + stats
     │ analyzer     │  │ :6379         │
     │ :5000        │  └───────┬───────┘
     └──────────────┘          │
                       ┌───────▼────────┐
                       │ Next.js dash   │
                       │ :3000          │
                       └────────────────┘
```

## Layout

```
.
├── docker-compose.yml       # Orchestrates the whole stack
├── gateway/
│   ├── Dockerfile
│   ├── go.mod / go.sum
│   ├── main.go              # Go proxy + Presidio tokenization core
│   └── main_test.go
└── dashboard/
    ├── Dockerfile
    ├── package.json
    └── src/app/             # Next.js App Router UI
```

## Prerequisites

- Docker with the Compose plugin (`docker compose version`).
- Optional: an `OPENAI_API_KEY` if you want to proxy to a real upstream.

## Quickstart

```bash
docker compose up --build
```

Then open:

- Dashboard — <http://localhost:3000>
- Gateway health — <http://localhost:8080/healthz>
- Presidio (direct) — <http://localhost:5000/health>

The gateway exposing `:8080` is the endpoint you point your LLM client at.

## Verify it works

Send a payload containing a phone number and an email:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"messages": [{"role": "user", "content": "Hello, my phone number is 555-0199 and my email is test@company.com"}]}'
```

By default the stack runs with `MOCK_UPSTREAM=true`, so the gateway **echoes the
sanitized payload** instead of forwarding it — no API key required:

```json
{
  "aiguard": "sanitized",
  "upstream": "mock",
  "messages": [
    { "role": "user", "content": "Hello, my phone number is [MASKED_PHONE_NUMBER_…] and my email is [MASKED_EMAIL_ADDRESS_…]" }
  ]
}
```

Refresh <http://localhost:3000> and both counters (**Total Evaluated Traffic** and
**PII Leaks Blocked**) increase. The dashboard auto-refreshes every 5 seconds.

### Proxying for real

```bash
export OPENAI_API_KEY=sk-...
export MOCK_UPSTREAM=false
docker compose up --build
```

Requests to `/v1/chat/completions` are now sanitized and forwarded to
`https://api.openai.com`. If the client does not send its own `Authorization`
header, the gateway injects `Bearer $OPENAI_API_KEY`.

## Configuration

Set these on the `gateway` service (environment variables):

| Variable         | Default                  | Purpose                                                    |
| ---------------- | ------------------------ | ---------------------------------------------------------- |
| `REDIS_URL`      | `redis://localhost:6379` | Where tokens and counters live.                            |
| `PRESIDIO_URL`   | `http://localhost:3000`  | Presidio analyzer base URL.                                |
| `UPSTREAM_URL`   | `https://api.openai.com` | Any OpenAI-compatible base URL.                            |
| `OPENAI_API_KEY` | _(empty)_                | Injected as a bearer token when the client sends none.     |
| `MOCK_UPSTREAM`  | `false`                  | `true` echoes the sanitized payload instead of forwarding. |

The `dashboard` service reads `REDIS_URL`.

## How tokenization works

1. The gateway reads the JSON body of `POST /v1/chat/completions`.
2. It sends the last message's text to Presidio's `/analyze`.
3. Findings scoring `>= 0.6` are replaced with `[MASKED_<ENTITY>_<n>]`; the
   original value is stored in Redis under that token with a 1-hour TTL.
4. The rewritten payload is forwarded (or echoed in mock mode).
5. Counters `stats:total_requests` and `stats:leaks_blocked` are incremented.

If Presidio is unreachable the gateway **fails open**: it forwards the original
text rather than dropping the request, and logs the failure.

## Notes on the implementation

- Presidio's analyzer listens on port **3000 inside its container**, so Compose
  maps `5000:3000` and the gateway uses `http://presidio:3000`.
- The maintained analyzer image is `ghcr.io/data-privacy-stack/presidio-analyzer`
  (the old `mcr.microsoft.com` image is a stale legacy tag).
- Each finding gets a unique token, so the same value appearing twice produces
  two independent tokens.
- The tokenizer walks Presidio's byte offsets left-to-right, so earlier
  replacements never shift (or corrupt) later spans.

## Local development (without Docker)

```bash
# gateway
cd gateway && go run .            # needs Redis + Presidio on localhost

# dashboard
cd ../dashboard && npm install && npm run dev
```

Or just use the compose stack — it wires all four services together.
