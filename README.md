# 🛡️ Guardian

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
              │   │ (token vault)│               │ counters                      │
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
2. **Evaluate** — the Go gateway extracts the payload text and checks it against the local **Microsoft Presidio NLP engine** container.
3. **Tokenize** — if sensitive information is detected (e.g. an email address), the gateway stores the original string inside an in-memory **Redis cache** and generates a unique, non-colliding placeholder token (e.g. `[MASKED_EMAIL_ADDRESS_1759…]`).
4. **Forward** — the sanitized text is routed safely to the upstream AI provider.

> **Roadmap — rehydrate.** Mapping placeholder tokens back to the real strings in the upstream *response* is a planned milestone and is **not implemented yet**. Today the gateway sanitizes requests and passes responses through unchanged.

### Detection scope

Guardian masks whatever entity types the Presidio analyzer is configured to recognize — out of the box that is PII such as `EMAIL_ADDRESS`, `PHONE_NUMBER`, `PERSON`, `CREDIT_CARD`, `IBAN_CODE`, `IP_ADDRESS`, `US_SSN` and many more (see the [Presidio supported entities](https://microsoft.github.io/presidio/supported_entities/)).

Secrets such as AWS access keys are **not** detected by Presidio's default recognizers. To cover your own secret formats, register a custom recognizer with Presidio; Guardian will mask whatever it returns.

---

## 🛠️ Tech Stack & Service Layout

- **Gateway Core:** Go (Golang) — `net/http` multiplexer over a `net/http/httputil` reverse proxy, built for sub-millisecond in-process overhead.
- **NLP Intelligence:** Microsoft Presidio Analyzer (`ghcr.io/data-privacy-stack/presidio-analyzer`) running on a dedicated micro-service port.
- **Token Storage:** Redis 7 (Alpine) — for lightning-fast key-value mapping and data expirations.
- **Metrics Visualization:** Next.js (TailwindCSS) — an administrative web UI tracking total request volume, blocked leaks, and block rate.

---

## ⚡ Quick Start (1-Command Setup)

### Prerequisites

Ensure you have [Docker](https://docker.com) and Docker Compose installed.

### 1. Launch the Stack

Clone the repository and spin up all 4 micro-services simultaneously:

```bash
git clone https://github.com/Flanxiyum001/Guardian.git
cd Guardian
docker compose up --build
```

### 2. Access the Admin Dashboard

Open your browser and navigate to **`http://localhost:3000`** to view real-time data metrics and leaks caught.

### 3. Run the Local Test

By default, the gateway runs with `MOCK_UPSTREAM=true` so you can smoke-test it completely free without needing a paid OpenAI API key. Fire a terminal request containing a leaked email and phone number directly into the proxy:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{
    "messages": [
      {
        "role": "user",
        "content": "Deploy for client jane.doe@acme.com or call 415-555-0132"
      }
    ]
  }'
```

**The intercepted proxy output:**

```json
{
  "guardian": "sanitized",
  "upstream": "mock",
  "messages": [
    {
      "role": "user",
      "content": "Deploy for client [MASKED_EMAIL_ADDRESS_1759…] or call [MASKED_PHONE_NUMBER_1759…]"
    }
  ]
}
```

*Check your browser at `http://localhost:3000` — the statistics cards will have instantly updated!*

With `MOCK_UPSTREAM=false` and `OPENAI_API_KEY` set, the same request is sanitized and forwarded to `https://api.openai.com` instead of being echoed.

---

## 🔒 Production Considerations

### Serverless Infrastructure Optimization

For cloud-scale orchestration or production operations running outside of Docker Compose, managing an elastic, self-hosted Redis instance can be costly.

This repository natively supports **Upstash Redis** — a serverless, zero-configuration key-value storage engine. The Go gateway (`go-redis`) and the dashboard (`node-redis`) both speak the standard Redis protocol, so point them at Upstash's **TLS endpoint**:

1. Provision a free database cluster on [Upstash](https://upstash.com).
2. Override your environment flags in your orchestration profile:

```env
REDIS_URL=rediss://default:<password>@<region>.upstash.io:6379
```

> Use Upstash's *TLS / Redis* connection string (it starts with `rediss://`), not the REST URL — the REST API needs a different client library than the ones this stack uses.

---

## 🤝 Contributing

Contributions are what make the open-source community an amazing place to learn, inspire, and create.

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
