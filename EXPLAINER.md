# cipher-route: The Plain-English Guide

## What does this do?

When you run multiple local AI tools on your computer, you run into three annoying problems:
1. **Port clutter:** Each tool wants its own network port (`8079`, `8081`, `8082`, etc.), creating configuration conflicts and opening your machine to the local network.
2. **Repeated calculations:** If you or an AI agent ask the same question twice, the model recalculates the entire response from scratch, wasting electricity and time.
3. **Crashes when local models go down:** If your local Ollama instance runs out of memory or crashes, all your tools break.

`cipher-route` is the front door for your entire AI setup:
* **One clean port:** It exposes `http://localhost:8080`. All internal services communicate through secure, invisible Unix domain sockets behind the scenes.
* **Instant memory cache:** If an incoming prompt has a similar meaning to an answer it already computed, it returns the cached response in **0.5 milliseconds** without recalculating.
* **Smart cloud backup:** It tries to run your request locally first. If your local model is offline, it automatically reroutes the prompt to real cloud providers (like Groq or OpenAI) using built-in circuit breakers.
* **Password protection:** On first boot, it generates a secret key in `~/.cipher/token`. Anyone trying to connect without this key is rejected with a `401 Unauthorized` error.

---

## What happens if I don't use this?

Without `cipher-route`, you must juggle multiple port numbers across different terminal windows, repeat identical computations, and handle local model crashes manually.

---

## Frequently Asked Questions

#### How do I view the visual dashboard?
Open your web browser and navigate to:
```text
http://localhost:8080/hud
```
This shows you live cluster health, latency graphs, and recent routing decisions.

#### Does this send my prompts to the cloud by default?
No. By default, it sends requests to your local Ollama instance on your machine. Cloud fallback only occurs if you explicitly provide cloud API keys.

#### How do I run it?
In the full Cipher system, `cipher appliance` starts `cipher-route` automatically. To run it alone:
```bash
PORT=8080 ./cipher-route
```
