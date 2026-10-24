"use client";

import React, { useEffect, useState } from "react";

interface MetricsData {
  cache_hits: number;
  cache_misses: number;
  total_requests: number;
  cache_hit_rate: string;
  fallback_events: number;
  token_threshold: number;
  similarity_cutoff: number;
  timestamp: string;
}

export default function Dashboard() {
  const [metrics, setMetrics] = useState<MetricsData | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    const fetchMetrics = async () => {
      try {
        const res = await fetch("http://localhost:8080/api/metrics");
        if (res.ok) {
          const data = await res.json();
          setMetrics(data);
        }
      } catch (err) {
        // Gateway offline or starting up
      } finally {
        setLoading(false);
      }
    };

    fetchMetrics();
    const interval = setInterval(fetchMetrics, 2000);
    return () => clearInterval(interval);
  }, []);

  return (
    <div style={{ maxWidth: 1100, margin: "0 auto", padding: "40px 20px", fontFamily: "sans-serif" }}>
      <header style={{ marginBottom: 32, borderBottom: "1px solid #1e293b", paddingBottom: 16 }}>
        <h1 style={{ fontSize: 28, margin: 0, fontWeight: 700 }}>MACR Telemetry & Semantic Cache Dashboard</h1>
        <p style={{ color: "#94a3b8", marginTop: 6, fontSize: 14 }}>
          Model-Agnostic Context Router | Token Budget Gateway & Fallback Monitor
        </p>
      </header>

      <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(220px, 1fr))", gap: 20 }}>
        <div style={{ background: "#0f172a", border: "1px solid #1e293b", padding: 20, borderRadius: 8 }}>
          <div style={{ fontSize: 13, color: "#94a3b8" }}>Cache Hit Rate</div>
          <div style={{ fontSize: 32, fontWeight: 700, color: "#38bdf8", marginTop: 8 }}>
            {metrics?.cache_hit_rate || "0.0%"}
          </div>
          <div style={{ fontSize: 12, color: "#64748b", marginTop: 4 }}>Similarity Threshold: &gt;= 0.95</div>
        </div>

        <div style={{ background: "#0f172a", border: "1px solid #1e293b", padding: 20, borderRadius: 8 }}>
          <div style={{ fontSize: 13, color: "#94a3b8" }}>Cache Hits (Saved Tokens)</div>
          <div style={{ fontSize: 32, fontWeight: 700, color: "#4ade80", marginTop: 8 }}>
            {metrics?.cache_hits ?? 0}
          </div>
          <div style={{ fontSize: 12, color: "#64748b", marginTop: 4 }}>Total Requests: {metrics?.total_requests ?? 0}</div>
        </div>

        <div style={{ background: "#0f172a", border: "1px solid #1e293b", padding: 20, borderRadius: 8 }}>
          <div style={{ fontSize: 13, color: "#94a3b8" }}>Ollama Fallbacks</div>
          <div style={{ fontSize: 32, fontWeight: 700, color: "#fbbf24", marginTop: 8 }}>
            {metrics?.fallback_events ?? 0}
          </div>
          <div style={{ fontSize: 12, color: "#64748b", marginTop: 4 }}>Rerouted to OmniRoute Cloud</div>
        </div>

        <div style={{ background: "#0f172a", border: "1px solid #1e293b", padding: 20, borderRadius: 8 }}>
          <div style={{ fontSize: 13, color: "#94a3b8" }}>Token Threshold</div>
          <div style={{ fontSize: 32, fontWeight: 700, color: "#a855f7", marginTop: 8 }}>
            {metrics?.token_threshold ?? 1000}
          </div>
          <div style={{ fontSize: 12, color: "#64748b", marginTop: 4 }}>&lt; 1k: Ollama | &gt;= 1k: Cloud</div>
        </div>
      </div>

      <div style={{ marginTop: 40, background: "#0f172a", border: "1px solid #1e293b", borderRadius: 8, padding: 24 }}>
        <h3 style={{ margin: "0 0 16px 0", fontSize: 18 }}>System Routing Topology</h3>
        <p style={{ color: "#94a3b8", fontSize: 14, lineHeight: 1.6 }}>
          1. <strong>Semantic Cache Layer:</strong> Incoming prompts are vectorized into 128-dim cosine embeddings. Matches &gt;= 0.95 return instantly without upstream token consumption.<br />
          2. <strong>Local Ollama Routing:</strong> Prompts &lt; 1,000 tokens route to local hardware (11434).<br />
          3. <strong>Automatic Fallback:</strong> If local Ollama times out or errors, traffic instantly reroutes to OmniRoute Cloud.
        </p>
      </div>
    </div>
  );
}
