// Package notification 的通知端点(原混在 feature.go):角标/下拉列表/已读回执。
package notification

import (
	"net/http"
	"strconv"

	"visualink/internal/platform/auth"
)

// MarkAllNotificationsRead handles POST /notifications/read-all
func MarkAllNotificationsRead(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r)
		if err := d.Repo.MarkAllNotificationsRead(u.ID); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Return empty badge (no more unread)
		if err := partials.ExecuteTemplate(w, "notif_read_response.html", nil); err != nil {
			http.Error(w, err.Error(), 500)
		}
	}
}

// GetNotificationBadge handles GET /notifications/count — returns badge HTML for nav bell.
func GetNotificationBadge(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r)
		notifs, err := d.Repo.ListUnreadNotifications(u.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := partials.ExecuteTemplate(w, "notif_badge.html", notifs); err != nil {
			http.Error(w, err.Error(), 500)
		}
	}
}

// GetNotificationList handles GET /notifications — returns dropdown list HTML.
func GetNotificationList(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r)
		notifs, err := d.Repo.ListUnreadNotifications(u.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := partials.ExecuteTemplate(w, "notif_list.html", notifs); err != nil {
			http.Error(w, err.Error(), 500)
		}
	}
}

// MarkNotificationsRead handles POST /notifications/read — marks feature's notifs as read,
// returns updated badge + list HTML via OOB swap.
func MarkNotificationsRead(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r)
		notifIDStr := r.FormValue("id")
		if notifIDStr != "" {
			notifID, err := strconv.ParseInt(notifIDStr, 10, 64)
			if err != nil {
				http.Error(w, "invalid id", 400)
				return
			}
			if err := d.Repo.MarkNotificationReadByID(u.ID, notifID); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		} else {
			featureIDStr := r.FormValue("feature_id")
			featureID, err := strconv.ParseInt(featureIDStr, 10, 64)
			if err != nil {
				http.Error(w, "invalid feature_id", 400)
				return
			}
			if err := d.Repo.MarkNotificationsReadByFeature(u.ID, featureID); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		notifs, err := d.Repo.ListUnreadNotifications(u.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// Return badge update + list update via OOB
		if err := partials.ExecuteTemplate(w, "notif_read_response.html", notifs); err != nil {
			http.Error(w, err.Error(), 500)
		}
	}
}
