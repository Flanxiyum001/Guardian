# Guardian

Guardian is an open-source, **enterprise-grade API gateway and network proxy** that detects and tokenizes Personally Identifiable Information (PII) before it leaves your secure infrastructure and reaches external LLM providers (like OpenAI, Claude, or Gemini).

### 🚀 The Problem

Modern companies want to give employees access to generative AI to boost productivity. However, developers and business units frequently copy-paste proprietary code, API keys, client emails, and confidential metrics into cloud AI endpoints. Blanket-blocking AI ruins innovation, but unchecked usage risks catastrophic data compliance violations (GDPR, HIPAA, SOC2).

**Guardian fixes this by acting as a low-latency, local sanitization barrier.**

---

## 🏗️ Architecture Overview

Guardian is a central proxy that sits seamlessly between your internal developer network and the public AI APIs.

```text
              ┌──────────────────────────── Docker stack ────────────────────────────┐
              │                                                                      │
 [ Developer ]│   ┌──────────────┐   scan text   ┌────────────────┐                 │
 :8080 ───────┼──►│  Go Gateway  │──────────────►│    Presidio    │                 │
              │   │   (proxy)    │◄──────────────│   local NLP    │                 │
              │   └──────┬───────┘   findings    └────────────────┘                 │
              │          │ tokens                                                  │
              │          ▼                                                         │
              │   ┌──────────────┐                                               │
              │   │ Redis cache  │◄──────────────┐                               │
              │   │ token vault  │               │ counters, audit, budgets      │
              │   └──────────────┘               │                               │
              │                                  │                               │
 [ Security ] │   ┌──────────────┐               │                               │
 :3000 ───────┼──►│  Next.js UI  │───────────────┘                               │
              │   └──────────────┘                                               │
              └──────────────────────────┬───────────────────────────────────────┘
                                         │ sanitized payload (TLS)
                                         ▼
                              [ External AI provider ]
```

1. **Intercept** — the developer targets `http://localhost:8080/v1/chat/completions` instead of `api.openai.com`.
2. **Evaluate** — the Go gateway checks the caller's JWT for their role/team, then scans each prompt against the local **Microsoft Presidio NLP engine** *and* your enterprise dictionary regexes.
3. **Tokenize** — every entity the caller is *not* cleared to send is replaced with a unique token (e.g. `[MASKED_EMAIL_ADDRESS_1759…]`); the real value is stored in **Redis** with a TTL.
4. **Forward** — the sanitized payload goes upstream. Streaming (SSE) responses are passed through **chunk-by-chunk**.
5. **Rehydrate** — on the way back the gateway swaps each token for its original value, so the developer sees clean text while the provider never saw the PII.

### Detection scope

Guardian masks whatever the Presidio analyzer recognizes — out of the box `EMAIL_ADDRESS`, `PHONE_NUMBER`, `PERSON`, `CREDIT_CARD`, `IBAN_CODE`, `IP_ADDRESS`, `US_SSN` and many more ([full list](https://microsoft.github.io/presidio/supported_entities/)) — **plus** anything matched by your own `config/custom-rules.json` regexes (project codenames, internal ticket IDs, API-key formats, …). Presidio alone does not detect AWS keys or API secrets; add a custom rule for those.

---

## 🔐 Role-Based Masking

Not everything is masked for everyone. The gateway reads a signed **JWT** from the `Authorization: Bearer …` header and applies the matching policy:

```json
{
  "role_policy": {
    "default": { "allow_entities": [] },
    "roles": {
      "data-science": { "allow_entities": ["ZIP_CODE"] },
      "marketing": { "allow_entities": [] }
    }
  }
}
```

A data-science analyst can send customer ZIP codes; a marketing intern sending the same prompt has them masked. Identity comes from the `role`, `team` and `groups` claims, verified with `JWT_SECRET` (HS256/384/512).

> For local testing only, set `ALLOW_ROLE_HEADER=true` to trust `X-Guardian-Role` / `X-Guardian-Team` headers instead of a JWT. Never enable it in production.

## 📖 Custom Enterprise Dictionary + Live Reload

`gateway/config/custom-rules.json` defines regex rules, the role policy, budgets and pricing. The gateway watches the file with **fsnotify** and reloads on save — no container restart:

```json
{
  "rules": [
    { "id": "project-codenames", "entity_type": "PROJECT_CODENAME", "score": 0.9,
      "patterns": ["Project\\s+[A-Z][A-Za-z0-9]+", "Secret-V\\d+"] }
  ]
}
```

The compose stack bind-mounts `./gateway/config` into the container, so save the file on the host and the next request uses the new rules.

## ⚡ Streaming & Failure Policy

- **True streaming.** `text/event-stream` responses are rehydrated in flight. A token split across two chunks is buffered until it completes, so rehydration never corrupts the stream. Non-streaming bodies are buffered and rewritten.
- **`FAILURE_POLICY`** decides what happens when Presidio is unreachable:
  - `BLOCK` (default, fail-closed) → `503` and an alert on the dashboard.
  - `PASS` (fail-open) → the request is forwarded using only the local custom rules, and an alert is raised.

## 💸 Cost Tracking & Budget Caps

The gateway maps token usage to dollars using the `pricing` table in the config, records spend per team under `budget:<team>:<YYYY-MM>`, and returns `429` when a cap is hit:

```json
{ "error": { "message": "AI Budget Exceeded. Contact your administrator.",
             "type": "guardian_budget_exceeded" } }
```

Usage comes from the upstream `usage` object when the provider returns one; otherwise it is estimated from the payload size.

## 🔍 PII Leak Audit Log

Every request is appended to a capped Redis list and rendered at `/audit`:
`Timestamp | User | IP | Team/Role | Service | Status | Entities masked | Preview`.

The table **only ever shows masked tokens**. An authorized officer enters the `AUDIT_REVEAL_KEY` and can reveal the original values, which are fetched server-side from Redis (`POST /api/reveal`).

---

## 🛠️ Tech Stack

- **Gateway Core:** Go (Golang) — `net/http` multiplexer over a `net/http/httputil` reverse proxy with a custom streaming rehydrator.
- **NLP Intelligence:** Microsoft Presidio Analyzer (`ghcr.io/data-privacy-stack/presidio-analyzer`).
- **Token Storage:** Redis 7 (Alpine) — token vault, counters, audit log, budgets, alerts.
- **Auth/Policies:** `golang-jwt/jwt` + `fsnotify` for hot-reloaded rules.
- **Metrics Visualization:** Next.js (TailwindCSS) — overview, cost and audit pages.

---

## ⚡ Quick Start (1-Command Setup)

### Prerequisites

Ensure you have [Docker](https://docker.com) and Docker Compose installed.

### 1. Launch the Stack

```bash
git clone https://github.com/Flanxiyum001/Guardian.git
cd Guardian
docker compose up --build
```

### 2. Access the Admin Dashboard

- <http://localhost:3000> — overview (traffic, leaks, spend, alerts)
- <http://localhost:3000/costs> — per-team budgets
- <http://localhost:3000/audit> — leak audit log + reveal

### 3. Run the Local Test

With the default `MOCK_UPSTREAM=true` you can smoke-test without an API key:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "messages": [
      { "role": "user",
        "content": "Deploy Project Aurora for client jane.doe@acme.com in 94107" }
    ]
  }'
```

The response shows the sanitized payload sent upstream plus what the client gets back after rehydration:

```json
{
  "guardian": "sanitized",
  "upstream": "mock",
  "messages": [
    { "role": "user",
      "content": "Deploy [MASKED_PROJECT_CODENAME_…] for client [MASKED_EMAIL_ADDRESS_…] in [MASKED_ZIP_CODE_…]" }
  ],
  "rehydrated_messages": [
    { "role": "user",
      "content": "Deploy Project Aurora for client jane.doe@acme.com in 94107" }
  ]
}
```

Try the same prompt as a data-science caller (local header mode) and the ZIP code is left intact:

```bash
ALLOW_ROLE_HEADER=true docker compose up -d   # one-off: enable dev role headers
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "X-Guardian-Team: data-science" \
  -d '{"messages":[{"role":"user","content":"zip 94107 for Project Aurora"}]}'
```

---

## 🔒 Production Considerations

### Serverless Infrastructure Optimization

Managing a self-hosted Redis instance can be costly. This repo natively supports **Upstash Redis** — point both the Go gateway (`go-redis`) and the dashboard (`node-redis`) at its **TLS endpoint**:

```env
REDIS_URL=rediss://default:<password>@<region>.upstash.io:6379
```

> Use Upstash's *TLS / Redis* connection string (starting with `rediss://`), not the REST URL.

### Configuration reference

| Variable | Default | Purpose |
| --- | --- | --- |
| `REDIS_URL` | `redis://localhost:6379` | Token vault, counters, audit, budgets. |
| `PRESIDIO_URL` | `http://localhost:3000` | Presidio analyzer base URL. |
| `UPSTREAM_URL` | `https://api.openai.com` | Any OpenAI-compatible base URL. |
| `OPENAI_API_KEY` | _(empty)_ | Injected as a bearer token when the client sends none. |
| `MOCK_UPSTREAM` | `false` | Echo the sanitized payload instead of forwarding. |
| `FAILURE_POLICY` | `BLOCK` | `BLOCK` (fail-closed) or `PASS` (fail-open) when Presidio is down. |
| `JWT_SECRET` | _(empty)_ | HS256/384/512 secret for verifying caller JWTs. |
| `ALLOW_ROLE_HEADER` | `false` | Trust `X-Guardian-Role`/`X-Guardian-Team` (local testing only). |
| `CONFIG_PATH` | `config/custom-rules.json` | Hot-reloaded rules/policy/budgets/pricing. |
| `TOKEN_TTL_HOURS` | `24` | How long originals stay retrievable in Redis. |
| `AUDIT_LOG_SIZE` | `500` | Audit rows retained. |
| `AUDIT_REVEAL_KEY` | _(empty)_ | Dashboard: officer key required to reveal originals. |

---

## 🤝 Contributing

1. Fork the Project.
2. Create your Feature Branch (`git checkout -b feature/AmazingFeature`).
3. Commit your Changes (`git commit -m 'Add some AmazingFeature'`).
4. Push to the Branch (`git push origin feature/AmazingFeature`).
5. Open a Pull Request.

### Running the checks

```bash
cd gateway && go vet ./... && go test ./...
cd ../dashboard && npm install && npm run build
```

---

## 📄 License

MIT — see [LICENSE](LICENSE).
