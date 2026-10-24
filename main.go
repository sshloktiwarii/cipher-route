package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
)

// Config represents runtime gateway parameters
type Config struct {
	Port              string
	OllamaURL         string
	CloudURL          string
	RedisAddr         string
	RedisPassword     string
	RateLimitRPM      int
	TokenThreshold    int
	TelemetryEndpoint string
}

// OpenAIChatCompletionRequest matches the OpenAI API payload
type OpenAIChatCompletionRequest struct {
	Model       string                   `json:"model"`
	Messages    []OpenAIChatMessage      `json:"messages"`
	Temperature *float64                 `json:"temperature,omitempty"`
	MaxTokens   *int                     `json:"max_tokens,omitempty"`
	Stream      bool                     `json:"stream,omitempty"`
	Extra       map[string]interface{}   `json:"-"`
}

// OpenAIChatMessage represents a message in the conversation
type OpenAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

// TelemetryPayload represents the contract for Project 4 (Telemetry Engine)
type TelemetryPayload struct {
	Source    string                 `json:"source"`
	EventType string                 `json:"event_type"`
	Metrics   map[string]float64     `json:"metrics"`
	Metadata  map[string]string      `json:"metadata"`
	Timestamp string                 `json:"timestamp"`
}

// RateLimiter manages client quotas with Redis and in-memory fallback
type RateLimiter struct {
	client       *redis.Client
	useFallback  bool
	limitRPM     int
	fallbackLock sync.Mutex
	localCounts  map[string]int
	windowReset  time.Time
}

func NewRateLimiter(redisAddr, password string, rpm int) *RateLimiter {
	rl := &RateLimiter{
		limitRPM:    rpm,
		localCounts: make(map[string]int),
		windowReset: time.Now().Add(time.Minute),
	}

	if redisAddr != "" {
		rdb := redis.NewClient(&redis.Options{
			Addr:        redisAddr,
			Password:    password,
			DB:          0,
			DialTimeout: 500 * time.Millisecond,
		})
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		if err := rdb.Ping(ctx).Err(); err == nil {
			log.Println("[INFO] Connected to Redis for distributed rate limiting")
			rl.client = rdb
			return rl
		}
		log.Printf("[WARN] Redis unreachable at %s; engaging in-memory token fallback", redisAddr)
	}

	rl.useFallback = true
	return rl
}

func (rl *RateLimiter) Allow(clientIP string) (bool, error) {
	if !rl.useFallback && rl.client != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()

		key := fmt.Sprintf("rl:macr:%s:%d", clientIP, time.Now().Unix()/60)
		count, err := rl.client.Incr(ctx, key).Result()
		if err != nil {
			log.Printf("[WARN] Redis Incr failed (%v), falling back to in-memory limiter", err)
			return rl.allowInMemory(clientIP), nil
		}
		if count == 1 {
			_ = rl.client.Expire(ctx, key, 70*time.Second)
		}
		return count <= int64(rl.limitRPM), nil
	}

	return rl.allowInMemory(clientIP), nil
}

func (rl *RateLimiter) allowInMemory(clientIP string) bool {
	rl.fallbackLock.Lock()
	defer rl.fallbackLock.Unlock()

	now := time.Now()
	if now.After(rl.windowReset) {
		rl.localCounts = make(map[string]int)
		rl.windowReset = now.Add(time.Minute)
	}

	rl.localCounts[clientIP]++
	return rl.localCounts[clientIP] <= rl.limitRPM
}

// RouterGateway implements reverse proxy logic based on token count
type RouterGateway struct {
	cfg         Config
	limiter     *RateLimiter
	ollamaURL   *url.URL
	cloudURL    *url.URL
	httpClient  *http.Client
}

func NewRouterGateway(cfg Config) (*RouterGateway, error) {
	ollamaParsed, err := url.Parse(cfg.OllamaURL)
	if err != nil {
		return nil, fmt.Errorf("invalid OLLAMA_URL: %w", err)
	}

	cloudParsed, err := url.Parse(cfg.CloudURL)
	if err != nil {
		return nil, fmt.Errorf("invalid CLOUD_URL: %w", err)
	}

	return &RouterGateway{
		cfg:        cfg,
		limiter:    NewRateLimiter(cfg.RedisAddr, cfg.RedisPassword, cfg.RateLimitRPM),
		ollamaURL:  ollamaParsed,
		cloudURL:   cloudParsed,
		httpClient: &http.Client{Timeout: 5 * time.Second},
	}, nil
}

// EstimateTokens calculates approximate token size for OpenAI messages (~4 chars per token)
func EstimateTokens(req *OpenAIChatCompletionRequest) int {
	totalChars := 0
	for _, m := range req.Messages {
		totalChars += len(m.Role) + len(m.Content) + len(m.Name) + 4
	}
	tokens := totalChars / 4
	if tokens < 1 && totalChars > 0 {
		return 1
	}
	return tokens
}

func getClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (gw *RouterGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Health check
	if r.URL.Path == "/healthz" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"healthy","service":"macr"}`))
		return
	}

	// Mock cloud upstream endpoint for testing
	if r.URL.Path == "/mock/cloud/v1/chat/completions" {
		gw.handleMockCloud(w, r)
		return
	}

	if r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}

	clientIP := getClientIP(r)
	allowed, err := gw.limiter.Allow(clientIP)
	if err != nil || !allowed {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"Rate limit exceeded","type":"rate_limit_error","code":429}}`))
		gw.emitTelemetry("rate_limited", 0, time.Since(start), "none", http.StatusTooManyRequests)
		return
	}

	// Buffer and inspect body
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, `{"error":{"message":"Failed to read request body"}}`, http.StatusBadRequest)
		return
	}
	_ = r.Body.Close()

	var chatReq OpenAIChatCompletionRequest
	if err := json.Unmarshal(bodyBytes, &chatReq); err != nil {
		http.Error(w, `{"error":{"message":"Malformed JSON payload"}}`, http.StatusBadRequest)
		return
	}

	tokens := EstimateTokens(&chatReq)
	var targetURL *url.URL
	var routeTarget string

	if tokens < gw.cfg.TokenThreshold {
		targetURL = gw.ollamaURL
		routeTarget = "local_ollama"
	} else {
		targetURL = gw.cloudURL
		routeTarget = "cloud_endpoint"
	}

	log.Printf("[ROUTE] Request from %s | Tokens: %d | Route: %s -> %s", clientIP, tokens, routeTarget, targetURL.String())

	// Prepare proxy
	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Director = func(req *http.Request) {
		req.Host = targetURL.Host
		req.URL.Scheme = targetURL.Scheme
		req.URL.Host = targetURL.Host
		req.URL.Path = targetURL.Path
		if req.URL.Path == "" {
			req.URL.Path = "/v1/chat/completions"
		}
		req.Header.Set("X-Forwarded-Host", r.Host)
		req.Header.Set("X-MACR-Tokens", strconv.Itoa(tokens))
		req.Header.Set("X-MACR-Route", routeTarget)
		req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		req.ContentLength = int64(len(bodyBytes))
	}

	proxy.ErrorHandler = func(rw http.ResponseWriter, req *http.Request, proxyErr error) {
		log.Printf("[ERROR] Proxy upstream error to %s: %v", targetURL.String(), proxyErr)
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusBadGateway)
		_, _ = rw.Write([]byte(fmt.Sprintf(`{"error":{"message":"Upstream destination unreachable: %v","target":"%s"}}`, proxyErr, routeTarget)))
		gw.emitTelemetry("upstream_error", tokens, time.Since(start), routeTarget, http.StatusBadGateway)
	}

	proxy.ModifyResponse = func(resp *http.Response) error {
		duration := time.Since(start)
		resp.Header.Set("X-MACR-Route-Selected", routeTarget)
		resp.Header.Set("X-MACR-Estimated-Tokens", strconv.Itoa(tokens))
		gw.emitTelemetry("request_routed", tokens, duration, routeTarget, resp.StatusCode)
		return nil
	}

	r.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	proxy.ServeHTTP(w, r)
}

func (gw *RouterGateway) handleMockCloud(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{
		"id": "chatcmpl-mock-cloud-999",
		"object": "chat.completion",
		"created": 1729800000,
		"model": "cloud-mock-model",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "This response was routed through the Cloud High-Capacity fallback endpoint (Tokens >= 1000)."
			},
			"finish_reason": "stop"
		}],
		"usage": {
			"prompt_tokens": 1050,
			"completion_tokens": 24,
			"total_tokens": 1074
		}
	}`))
}

func (gw *RouterGateway) emitTelemetry(eventType string, tokens int, latency time.Duration, route string, status int) {
	if gw.cfg.TelemetryEndpoint == "" {
		return
	}

	go func() {
		payload := TelemetryPayload{
			Source:    "macr-gateway",
			EventType: eventType,
			Metrics: map[string]float64{
				"token_count": float64(tokens),
				"latency_ms":  float64(latency.Milliseconds()),
				"http_status": float64(status),
			},
			Metadata: map[string]string{
				"route":  route,
				"status": strconv.Itoa(status),
			},
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		}

		body, err := json.Marshal(payload)
		if err != nil {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, gw.cfg.TelemetryEndpoint, bytes.NewBuffer(body))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := gw.httpClient.Do(req)
		if err == nil && resp != nil {
			_ = resp.Body.Close()
		}
	}()
}

func getEnv(key, defaultVal string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultVal
}

func main() {
	rpm, _ := strconv.Atoi(getEnv("RATE_LIMIT_RPM", "120"))
	threshold, _ := strconv.Atoi(getEnv("TOKEN_THRESHOLD", "1000"))

	cfg := Config{
		Port:              getEnv("PORT", "8080"),
		OllamaURL:         getEnv("OLLAMA_URL", "http://localhost:11434/v1/chat/completions"),
		CloudURL:          getEnv("CLOUD_URL", "http://localhost:8080/mock/cloud/v1/chat/completions"),
		RedisAddr:         getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:     getEnv("REDIS_PASSWORD", ""),
		RateLimitRPM:      rpm,
		TokenThreshold:    threshold,
		TelemetryEndpoint: getEnv("TELEMETRY_ENDPOINT", "http://localhost:8082/ingest"),
	}

	gw, err := NewRouterGateway(cfg)
	if err != nil {
		log.Fatalf("[FATAL] Gateway initialization failed: %v", err)
	}

	server := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      gw,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	go func() {
		log.Printf("[INIT] Model-Agnostic Context Router (MACR) listening on port :%s", cfg.Port)
		log.Printf("[INIT] Threshold: < %d tokens -> Ollama | >= %d tokens -> Cloud", cfg.TokenThreshold, cfg.TokenThreshold)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[FATAL] HTTP server error: %v", err)
		}
	}()

	<-stop
	log.Println("[SHUTDOWN] Gracefully terminating MACR gateway...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		log.Printf("[ERROR] Server forced to shutdown: %v", err)
	}
	log.Println("[SHUTDOWN] MACR server exited clean")
}
