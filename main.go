package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	SimilarityCutoff  float64
	TelemetryEndpoint string
}

// OpenAIChatCompletionRequest matches the OpenAI API payload
type OpenAIChatCompletionRequest struct {
	Model       string              `json:"model"`
	Messages    []OpenAIChatMessage `json:"messages"`
	Temperature *float64            `json:"temperature,omitempty"`
	MaxTokens   *int                `json:"max_tokens,omitempty"`
	Stream      bool                `json:"stream,omitempty"`
}

type OpenAIChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

type CachedResponse struct {
	Body       []byte
	Headers    http.Header
	Tokens     int
	CachedAt   time.Time
	Hits       int
	Vector     []float64
	PromptText string
}

// VectorSemanticCache provides in-memory vector embedding similarity matching
type VectorSemanticCache struct {
	mu        sync.RWMutex
	exactMap  map[string]*CachedResponse
	vectorLog []*CachedResponse
	dim       int
	hitCount  uint64
	missCount uint64
}

func NewVectorSemanticCache() *VectorSemanticCache {
	return &VectorSemanticCache{
		exactMap:  make(map[string]*CachedResponse),
		vectorLog: make([]*CachedResponse, 0, 500),
		dim:       128,
	}
}

// ComputeEmbedding generates a normalized 128-dim vector embedding using character n-gram hashing
func (c *VectorSemanticCache) ComputeEmbedding(text string) []float64 {
	vec := make([]float64, c.dim)
	normalized := strings.ToLower(strings.TrimSpace(text))
	runes := []rune(normalized)

	if len(runes) < 2 {
		return vec
	}

	for i := 0; i < len(runes)-1; i++ {
		bigram := string(runes[i : i+2])
		h := sha256.Sum256([]byte(bigram))
		idx := int(h[0]) % c.dim
		sign := 1.0
		if h[1]%2 == 0 {
			sign = -1.0
		}
		vec[idx] += sign * (float64(h[2]) / 255.0)
	}

	// L2 Normalize
	var sumSquares float64
	for _, val := range vec {
		sumSquares += val * val
	}
	if norm := math.Sqrt(sumSquares); norm > 0 {
		for i := range vec {
			vec[i] /= norm
		}
	}
	return vec
}

// CosineSimilarity computes the dot product of two L2-normalized vectors
func CosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot float64
	for i := range a {
		dot += a[i] * b[i]
	}
	return math.Max(0.0, math.Min(1.0, dot))
}

// FindMatch searches for an exact hash hit or semantic cosine match >= similarity threshold
func (c *VectorSemanticCache) FindMatch(prompt string, threshold float64) (*CachedResponse, float64) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	hash := sha256Hex(prompt)
	if match, exists := c.exactMap[hash]; exists {
		atomic.AddUint64(&c.hitCount, 1)
		match.Hits++
		return match, 1.0
	}

	queryVec := c.ComputeEmbedding(prompt)
	var bestMatch *CachedResponse
	var bestSim float64

	for _, item := range c.vectorLog {
		sim := CosineSimilarity(queryVec, item.Vector)
		if sim > bestSim {
			bestSim = sim
			bestMatch = item
		}
	}

	if bestSim >= threshold && bestMatch != nil {
		atomic.AddUint64(&c.hitCount, 1)
		bestMatch.Hits++
		return bestMatch, bestSim
	}

	atomic.AddUint64(&c.missCount, 1)
	return nil, bestSim
}

func (c *VectorSemanticCache) Put(prompt string, body []byte, headers http.Header, tokens int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	hash := sha256Hex(prompt)
	vec := c.ComputeEmbedding(prompt)

	entry := &CachedResponse{
		Body:       body,
		Headers:    headers.Clone(),
		Tokens:     tokens,
		CachedAt:   time.Now(),
		Vector:     vec,
		PromptText: prompt,
	}

	if len(c.vectorLog) >= 500 {
		old := c.vectorLog[0]
		delete(c.exactMap, sha256Hex(old.PromptText))
		c.vectorLog = c.vectorLog[1:]
	}
	c.exactMap[hash] = entry
	c.vectorLog = append(c.vectorLog, entry)
}

func sha256Hex(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
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

// RouterGateway implements reverse proxy logic with semantic cache and fallback
type RouterGateway struct {
	cfg          Config
	limiter      *RateLimiter
	cache        *VectorSemanticCache
	ollamaURL    *url.URL
	cloudURL     *url.URL
	httpClient   *http.Client
	fallbackHits uint64
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
		cache:      NewVectorSemanticCache(),
		ollamaURL:  ollamaParsed,
		cloudURL:   cloudParsed,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

func ExtractPromptText(req *OpenAIChatCompletionRequest) string {
	var sb strings.Builder
	for _, m := range req.Messages {
		sb.WriteString(m.Role)
		sb.WriteString(":")
		sb.WriteString(m.Content)
		sb.WriteString("\n")
	}
	return sb.String()
}

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

	// CORS headers for Next.js dashboard
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// 1. Healthcheck
	if r.URL.Path == "/healthz" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"healthy","service":"macr-maximized","semantic_cache":true}`))
		return
	}

	// 2. Metrics endpoint for Next.js Dashboard
	if r.URL.Path == "/api/metrics" {
		gw.handleDashboardMetrics(w, r)
		return
	}

	// 3. Mock cloud endpoint
	if r.URL.Path == "/mock/cloud/v1/chat/completions" {
		gw.handleMockCloud(w, r)
		return
	}

	if r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}

	// Rate limiting
	clientIP := getClientIP(r)
	allowed, err := gw.limiter.Allow(clientIP)
	if err != nil || !allowed {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"Rate limit exceeded","code":429}}`))
		gw.emitTelemetry("rate_limited", 0, time.Since(start), "none", http.StatusTooManyRequests)
		return
	}

	// Read & Parse payload with 2MB maximum limit
	r.Body = http.MaxBytesReader(w, r.Body, 2*1024*1024)
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

	promptText := ExtractPromptText(&chatReq)
	tokens := EstimateTokens(&chatReq)

	// 4. Feature 1: Semantic Caching
	if cached, similarity := gw.cache.FindMatch(promptText, gw.cfg.SimilarityCutoff); cached != nil {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-MACR-Cache", "HIT")
		w.Header().Set("X-MACR-Similarity", fmt.Sprintf("%.4f", similarity))
		w.Header().Set("X-MACR-Route-Selected", "semantic_cache")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(cached.Body)

		log.Printf("[CACHE HIT] Client: %s | Sim: %.4f | Saved Tokens: %d", clientIP, similarity, tokens)
		gw.emitTelemetry("cache_hit", tokens, time.Since(start), "semantic_cache", http.StatusOK)
		return
	}

	// 5. Feature 2: Fallback Routing (Try Ollama -> on failure / timeout fallback to Cloud)
	var resp *http.Response
	var routeTarget string
	var fallbackTriggered bool

	if tokens < gw.cfg.TokenThreshold {
		routeTarget = "local_ollama"
		// Try Ollama with low-latency timeout
		ollamaReq, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, gw.ollamaURL.String(), bytes.NewBuffer(bodyBytes))
		ollamaReq.Header = r.Header.Clone()

		ctxTimeout, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		resp, err = gw.httpClient.Do(ollamaReq.WithContext(ctxTimeout))
		cancel()

		// If Ollama is down, times out, or errors -> execute Fallback Routing
		if err != nil || (resp != nil && resp.StatusCode >= 500) {
			if resp != nil {
				_ = resp.Body.Close()
			}
			log.Printf("[FALLBACK ROUTING] Ollama offline or errored (%v). Rerouting to OmniRoute Cloud...", err)
			routeTarget = "cloud_fallback"
			fallbackTriggered = true
			atomic.AddUint64(&gw.fallbackHits, 1)

			cloudReq, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, gw.cloudURL.String(), bytes.NewBuffer(bodyBytes))
			cloudReq.Header = r.Header.Clone()
			resp, err = gw.httpClient.Do(cloudReq)
		}
	} else {
		routeTarget = "cloud_high_capacity"
		cloudReq, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, gw.cloudURL.String(), bytes.NewBuffer(bodyBytes))
		cloudReq.Header = r.Header.Clone()
		resp, err = gw.httpClient.Do(cloudReq)
	}

	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":"Upstream route failed: %v"}}`, err), http.StatusBadGateway)
		gw.emitTelemetry("upstream_error", tokens, time.Since(start), routeTarget, http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		http.Error(w, `{"error":{"message":"Failed to read upstream response"}}`, http.StatusBadGateway)
		return
	}

	// Cache successful responses for semantic caching
	if resp.StatusCode == http.StatusOK && !chatReq.Stream {
		gw.cache.Put(promptText, respBody, resp.Header, tokens)
	}

	// Write response headers and body
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.Header().Set("X-MACR-Cache", "MISS")
	w.Header().Set("X-MACR-Route-Selected", routeTarget)
	if fallbackTriggered {
		w.Header().Set("X-MACR-Fallback", "TRUE")
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBody)

	gw.emitTelemetry("request_routed", tokens, time.Since(start), routeTarget, resp.StatusCode)
}

func (gw *RouterGateway) handleDashboardMetrics(w http.ResponseWriter, r *http.Request) {
	hits := atomic.LoadUint64(&gw.cache.hitCount)
	misses := atomic.LoadUint64(&gw.cache.missCount)
	total := hits + misses
	hitRate := 0.0
	if total > 0 {
		hitRate = float64(hits) / float64(total) * 100.0
	}

	metrics := map[string]interface{}{
		"cache_hits":        hits,
		"cache_misses":      misses,
		"total_requests":    total,
		"cache_hit_rate":    fmt.Sprintf("%.1f%%", hitRate),
		"fallback_events":   atomic.LoadUint64(&gw.fallbackHits),
		"token_threshold":   gw.cfg.TokenThreshold,
		"similarity_cutoff": gw.cfg.SimilarityCutoff,
		"timestamp":         time.Now().UTC().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(metrics)
}

func (gw *RouterGateway) handleMockCloud(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{
		"id": "chatcmpl-omniroute-cloud",
		"object": "chat.completion",
		"created": 1729800000,
		"model": "omniroute-cloud-tier1",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"content": "Generated by OmniRoute Cloud Fallback Engine. High throughput guaranteed."
			},
			"finish_reason": "stop"
		}],
		"usage": {"prompt_tokens": 1050, "completion_tokens": 18, "total_tokens": 1068}
	}`))
}

func (gw *RouterGateway) emitTelemetry(eventType string, tokens int, latency time.Duration, route string, status int) {
	if gw.cfg.TelemetryEndpoint == "" {
		return
	}

	go func() {
		payload := map[string]interface{}{
			"source":     "macr-router-maximized",
			"event_type": eventType,
			"metrics": map[string]float64{
				"token_count": float64(tokens),
				"latency_ms":  float64(latency.Milliseconds()),
				"status":      float64(status),
			},
			"metadata": map[string]string{
				"route":  route,
				"status": strconv.Itoa(status),
			},
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		}

		body, _ := json.Marshal(payload)
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, gw.cfg.TelemetryEndpoint, bytes.NewBuffer(body))
		if err == nil {
			req.Header.Set("Content-Type", "application/json")
			resp, err := gw.httpClient.Do(req)
			if err == nil && resp != nil {
				_ = resp.Body.Close()
			}
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
	similarity, _ := strconv.ParseFloat(getEnv("SIMILARITY_CUTOFF", "0.95"), 64)

	cfg := Config{
		Port:              getEnv("PORT", "8080"),
		OllamaURL:         getEnv("OLLAMA_URL", "http://localhost:11434/v1/chat/completions"),
		CloudURL:          getEnv("CLOUD_URL", "http://localhost:8080/mock/cloud/v1/chat/completions"),
		RedisAddr:         getEnv("REDIS_ADDR", "localhost:6379"),
		RedisPassword:     getEnv("REDIS_PASSWORD", ""),
		RateLimitRPM:      rpm,
		TokenThreshold:    threshold,
		SimilarityCutoff:  similarity,
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
		log.Printf("[INIT] Maximized MACR Router listening on :%s", cfg.Port)
		log.Printf("[INIT] Features: Semantic Caching (Similarity >= %.2f) | Fallback Routing Active", cfg.SimilarityCutoff)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[FATAL] HTTP server error: %v", err)
		}
	}()

	<-stop
	log.Println("[SHUTDOWN] Terminating MACR Gateway...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}
