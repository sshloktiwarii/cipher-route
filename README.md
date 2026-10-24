# cipher-route

Unified HTTP ingress reverse proxy, semantic vector cache, and circuit breaker.

Written in pure Go. Eliminates host port sprawl by aggregating all backend microservices via Unix domain sockets in `/tmp/cipher/*.sock` behind port `8080`, secured with local cryptographic session token authorization.

Looking for a plain-English, non-technical summary? See [EXPLAINER.md](EXPLAINER.md).

---

## Routing Architecture

```text
HTTP Request (:8080) ---> [Token Auth Gate] ---> [Semantic Vector Cache]
                                                         |
                   +-------------------------------------+-------------------------------------+
                   | (Cache Hit: <0.5ms)                                                       | (Cache Miss)
                   v                                                                           v
         Return Cached JSON                                                     [Token Threshold Evaluator]
                                                                                               |
                                                 +---------------------------------------------+---------------------------------------------+
                                                 | (<1000 Tokens)                                                                            | (>=1000 Tokens)
                                                 v                                                                                           v
                                            Local Ollama                                                                             Cloud Circuit Breakers
                                        (3s Low-Latency Cutoff)                                                                     (Groq / OpenAI Key Rotators)
```

---

## Ingress Socket Map

All internal cluster services are proxied through `/tmp/cipher/*.sock` (`0600` permissions):

| Path Prefix | Target Socket | Underlying Service | Gate |
| :--- | :--- | :--- | :--- |
| `/hud` | Embedded HTML | Web Cockpit HUD | Public |
| `/cluster/status` | Internal Probe | Cluster Topology & Sockets | Public |
| `/verify-audit-chain` | `/tmp/cipher/log.sock` | Merkle Chain Integrity | Public |
| `/v1/chat/completions` | Local Ollama / Real Cloud | Fallback Gateway | Bearer Auth |
| `/api/sec/*` | `/tmp/cipher/sec.sock` | Semantic Firewall | Bearer Auth |
| `/api/exec/*` | `/tmp/cipher/exec.sock` | Seatbelt Sandbox | Bearer Auth |
| `/api/log/*` | `/tmp/cipher/log.sock` | Telemetry & Ledger | Bearer Auth |
| `/api/mem/*` | `/tmp/cipher/mem.sock` | Obsidian Vector Store | Bearer Auth |
| `/api/mcp/*` | `/tmp/cipher/mcp.sock` | MCP JSON-RPC Server | Bearer Auth |

---

## Build

```bash
go build -o cipher-route .
```

---

## Usage

```bash
PORT=8080 ./cipher-route
```

---

## License

MIT License. Copyright (c) 2026 Shlok Tiwari.
