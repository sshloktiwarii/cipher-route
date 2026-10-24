# ⚡ cipher-route — Unified Ingress Gateway, Semantic Cache & Circuit Breaker

[![Language](https://img.shields.io/badge/Language-Go%201.24-00ADD8?style=flat-square&logo=go)](https://go.dev/)
[![Status](https://img.shields.io/badge/Status-Production%20Ready-brightgreen?style=flat-square)](#)
[![Zero-Port-Sprawl](https://img.shields.io/badge/Networking-Zero--Port%20Sprawl-blueviolet?style=flat-square)](#)

The unified ingress traffic controller, semantic cache, and cloud circuit breaker for the **Cipher AI PaaS**. Eliminates port sprawl by proxying all 5 backend microservices via internal Unix Domain Sockets behind a single public port (`:8080`), protected by zero-trust session token authentication.

---

### 💡 For Non-Techies: What does this do?
> Imagine an intelligent air-traffic controller for all AI conversations on your computer:
>
> 1. **Zero Mess (One Door In)**: Instead of having 8 different confusing ports open on your machine, `cipher-route` gives you one clean front door (`http://localhost:8080`).
> 2. **Saves Time & Money (Instant Cache)**: If you ask the AI a question that was asked before, `cipher-route` remembers the answer and returns it in **0.5 milliseconds** without recalculating.
> 3. **Never Crashes (Smart Cloud Fallback)**: It tries to run your AI locally on your computer. If your local model is offline or out of memory, it automatically and seamlessly routes the request to cloud providers (like Groq or OpenAI) using real circuit breakers.
> 4. **Locked by Default**: Generates a private secret key on your machine (`~/.cipher/token`) so strangers on your WiFi cannot access your AI.

---

## Unified Ingress Routes (Zero-Port Sprawl)

All internal services communicate with `cipher-route` via Unix Domain Sockets (`/tmp/cipher/*.sock`), exposing only port `:8080`:

| Public Endpoint | Proxied Backend | Transport | Security Gate |
| :--- | :--- | :--- | :--- |
| `GET /hud` | Cockpit War Room Visual HUD | Embedded HTML5 | Public / Localhost |
| `GET /cluster/status` | Cluster Topology & Health | Internal Probe | Public / Localhost |
| `GET /verify-audit-chain` | Cryptographic Merkle Hash Chain | `/tmp/cipher/log.sock` | Public / Localhost |
| `POST /v1/chat/completions` | Local Ollama $\to$ Cloud Fallback | Real Upstream | Bearer Token Required |
| `ANY /api/sec/*` | `cipher-sec` Semantic Firewall | `/tmp/cipher/sec.sock` | Bearer Token Required |
| `ANY /api/exec/*` | `cipher-exec` Sandbox | `/tmp/cipher/exec.sock` | Bearer Token Required |
| `ANY /api/log/*` | `cipher-log` Merkle Ledger | `/tmp/cipher/log.sock` | Bearer Token Required |
| `ANY /api/mem/*` | `cipher-mem` Vector Store | `/tmp/cipher/mem.sock` | Bearer Token Required |
| `ANY /api/mcp/*` | `cipher-mcp` Action Server | `/tmp/cipher/mcp.sock` | Bearer Token Required |

---

## Quickstart

### Build Standalone Binary
```bash
go build -o cipher-route .
```

### Run Standalone
```bash
PORT=8080 ./cipher-route
```

### View Live Cockpit HUD
Open `http://localhost:8080/hud` in any web browser.

---

## License
MIT © [Shlok Tiwari](https://github.com/sshloktiwarii)
