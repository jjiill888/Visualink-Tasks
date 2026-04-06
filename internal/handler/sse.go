package handler

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"featuretrack/internal/hub"
)

// SSE handles GET /sse — keeps the connection open and streams events.
func SSE() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering if behind proxy

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		ch := hub.Global.Subscribe()
		defer hub.Global.Unsubscribe(ch)

		// Send an initial ping so the client knows it's connected.
		fmt.Fprintf(w, "event: ping\ndata: connected\n\n")
		flusher.Flush()

		// Heartbeat every 15s — aggressive enough to survive high-latency VPN
		// (180ms RTT) where idle connections are often culled by NAT/firewall.
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case event, ok := <-ch:
				if !ok {
					return
				}
				eventName, data, _ := strings.Cut(event, ":")
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", eventName, data)
				flusher.Flush()
			case <-ticker.C:
				fmt.Fprintf(w, ": heartbeat\n\n")
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	}
}
