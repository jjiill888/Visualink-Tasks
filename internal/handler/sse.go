package handler

import (
	"fmt"
	"net/http"

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

		for {
			select {
			case event, ok := <-ch:
				if !ok {
					return
				}
				fmt.Fprintf(w, "event: %s\ndata: \n\n", event)
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	}
}
