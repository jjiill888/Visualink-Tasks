package handler

import (
	"encoding/json"
	"fmt"
	"net/http"

	"featuretrack/internal/db"
	"featuretrack/internal/hub"
)

// CallSignal handles POST /im/call/signal — WebRTC signaling relay via SSE broadcast.
// Clients send offer/answer/ice/hangup payloads here; the server broadcasts them
// to all SSE subscribers and clients filter by the `to` field.
func CallSignal(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)

		var req struct {
			To        int64  `json:"to"`
			Type      string `json:"type"`      // offer | answer | ice | hangup
			SDP       string `json:"sdp"`       // offer / answer
			Candidate string `json:"candidate"` // ICE candidate string
			SdpMid    string `json:"sdpMid"`
			SdpMLine  *int   `json:"sdpMLineIndex"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid json", 400)
			return
		}
		if req.To == 0 || req.To == u.ID {
			http.Error(w, "invalid target", 400)
			return
		}
		switch req.Type {
		case "offer", "answer", "ice", "hangup":
		default:
			http.Error(w, "invalid type", 400)
			return
		}

		out := map[string]any{
			"to":       req.To,
			"from":     u.ID,
			"fromName": u.DisplayName,
			"type":     req.Type,
		}
		if req.SDP != "" {
			out["sdp"] = req.SDP
		}
		if req.Candidate != "" {
			out["candidate"] = req.Candidate
			out["sdpMid"] = req.SdpMid
			if req.SdpMLine != nil {
				out["sdpMLineIndex"] = *req.SdpMLine
			}
		}

		data, _ := json.Marshal(out)
		hub.Global.Broadcast(fmt.Sprintf("call-signal:%s", data))
		w.WriteHeader(http.StatusNoContent)
	}
}
