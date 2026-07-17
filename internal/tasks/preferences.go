package tasks

import (
	"net/http"

	"visualink/internal/platform/auth"
)

// Preferences handles GET /preferences — returns the preferences panel partial.
func Preferences(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r)
		groups, _ := d.Repo.ListSubscribedGroups(u.ID)
		features, _ := d.Repo.ListWatchedFeatures(u.ID)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = partials.ExecuteTemplate(w, "preferences_partial.html", map[string]any{
			"Groups":   groups,
			"Features": features,
		})
	}
}
