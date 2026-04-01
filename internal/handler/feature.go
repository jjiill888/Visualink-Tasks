package handler

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"featuretrack/internal/db"
	"featuretrack/internal/hub"
	"featuretrack/internal/model"

	"github.com/go-chi/chi/v5"
)

type dashboardData struct {
	Stats      *model.Stats
	Features   []featureRowData
	RecentDone []featureRowData
	Groups     []*model.Group
	Priority   string
	Status     string
}

func Dashboard(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stats, err := database.GetStats()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		features, err := database.ListFeatures("", "", "", nil, nil, nil)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		groups, err := database.ListGroups()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		u := UserFromContext(r)
		pd := pageData(r, "dashboard")
		pd.BannerMessage = fmt.Sprintf("共 %d 条待处理功能", stats.Pending)
		canEdit := canEditStatus(u.Role)
		featureRows := make([]featureRowData, len(features))
		for i, f := range features {
			featureRows[i] = featureRowData{Feature: f, CanEditStatus: canEdit}
		}
		var recentDone []featureRowData
		for _, f := range features {
			if f.Status == "done" {
				recentDone = append(recentDone, featureRowData{Feature: f, CanEditStatus: canEdit})
				if len(recentDone) >= 3 {
					break
				}
			}
		}
		pd.Data = dashboardData{
			Stats:      stats,
			Features:   featureRows,
			RecentDone: recentDone,
			Groups:     groups,
			Priority:   "all",
			Status:     "all",
		}
		render(w, r, "dashboard.html", pd)
	}
}

// ListFeatures is the HTMX partial endpoint — returns only the feature rows.
func ListFeatures(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		q := r.URL.Query()
		priority := q.Get("priority")
		status := q.Get("status")
		search := strings.TrimSpace(q.Get("search"))

		var groupID, assigneeID, creatorID *int64
		if v := q.Get("group_id"); v != "" {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
				groupID = &id
			}
		}
		if v := q.Get("assignee_id"); v != "" {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
				assigneeID = &id
			}
		}
		if v := q.Get("creator_id"); v != "" {
			if id, err := strconv.ParseInt(v, 10, 64); err == nil && id > 0 {
				creatorID = &id
			}
		}

		features, err := database.ListFeatures(priority, status, search, groupID, assigneeID, creatorID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		rows := make([]featureRowData, len(features))
		for i, f := range features {
			rows[i] = featureRowData{Feature: f, CanEditStatus: canEditStatus(u.Role)}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := PartialTmpl.ExecuteTemplate(w, "features_partial.html", rows); err != nil {
			http.Error(w, err.Error(), 500)
		}
	}
}

// UpdateStatus handles PATCH /features/{id}/status (HTMX)
func UpdateStatus(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		if !canEditStatus(u.Role) {
			http.Error(w, "forbidden", 403)
			return
		}
		idStr := chi.URLParam(r, "id")
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid id", 400)
			return
		}
		status := r.FormValue("status")
		if status != "in_progress" && status != "done" && status != "pending" && status != "rejected" {
			http.Error(w, "invalid status", 400)
			return
		}
		f, err := database.GetFeature(id)
		if err != nil || f == nil {
			http.Error(w, "not found", 404)
			return
		}
		oldStatus := f.Status
		if err := database.UpdateFeatureStatus(id, status); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		f, err = database.GetFeature(id)
		if err != nil || f == nil {
			http.Error(w, "not found", 404)
			return
		}
		_ = database.CreateFeatureEvent(&model.FeatureEvent{
			FeatureID:  id,
			OperatorID: u.ID,
			Action:     "status_changed",
			OldValue:   oldStatus,
			NewValue:   status,
		})
		hub.Global.Broadcast("feature-row-updated:" + idStr)
		hub.Global.Broadcast("stats-updated")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		row := featureRowData{Feature: f, CanEditStatus: true}
		if err := PartialTmpl.ExecuteTemplate(w, "feature_row.html", row); err != nil {
			http.Error(w, err.Error(), 500)
		}
	}
}

// GetStats handles GET /stats — returns stats grid partial + OOB banner update
func GetStats(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		stats, err := database.GetStats()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := PartialTmpl.ExecuteTemplate(w, "stats_partial.html", stats); err != nil {
			http.Error(w, err.Error(), 500)
		}
	}
}

// FeatureSubmitPage handles GET /features/submit — standalone full-page submit form.
func FeatureSubmitPage(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		groups, err := database.ListGroups()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		pd := pageData(r, "")
		pd.Data = struct{ Groups []*model.Group }{Groups: groups}
		render(w, r, "submit_standalone.html", pd)
	}
}

// FeatureForm handles GET /features/new — returns the submit form as a modal partial.
func FeatureForm(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		groups, err := database.ListGroups()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := PartialTmpl.ExecuteTemplate(w, "submit_form_modal.html", groups); err != nil {
			http.Error(w, err.Error(), 500)
		}
	}
}

// CreateFeature handles POST /features
func CreateFeature(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)

		title := strings.TrimSpace(r.FormValue("title"))
		description := strings.TrimSpace(r.FormValue("description"))
		priority := r.FormValue("priority")
		groupIDStr := r.FormValue("group_id")

		if title == "" {
			if r.Header.Get("HX-Request") == "true" {
				http.Error(w, "功能标题不能为空", http.StatusBadRequest)
				return
			}
			http.Redirect(w, r, "/dashboard?error=title_required", http.StatusSeeOther)
			return
		}
		if priority != "urgent" && priority != "high" && priority != "medium" && priority != "low" {
			priority = "medium"
		}

		f := &model.Feature{
			Title:       title,
			Description: description,
			Priority:    priority,
			Status:      "pending",
			CreatedBy:   u.ID,
		}
		if groupIDStr != "" && groupIDStr != "0" {
			gid, err := strconv.ParseInt(groupIDStr, 10, 64)
			if err == nil && gid > 0 {
				f.GroupID = &gid
			}
		}
		if err := database.CreateFeature(f); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		// 写入创建事件
		_ = database.CreateFeatureEvent(&model.FeatureEvent{
			FeatureID:  f.ID,
			OperatorID: u.ID,
			Action:     "created",
			OldValue:   "",
			NewValue:   "",
		})
		hub.Global.Broadcast("feature-list-changed")
		hub.Global.Broadcast("stats-updated")
		// HTMX request: return 200 so the client-side after-request handler can close the modal
		if r.Header.Get("HX-Request") == "true" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.Redirect(w, r, "/dashboard?success=1", http.StatusSeeOther)
	}
}

type mineData struct {
	Features []featureRowData
}

func Mine(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		features, err := database.ListFeaturesByUser(u.ID)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		canEdit := canEditStatus(u.Role)
		rows := make([]featureRowData, len(features))
		for i, f := range features {
			rows[i] = featureRowData{Feature: f, CanEditStatus: canEdit}
		}
		pd := pageData(r, "mine")
		pd.Data = mineData{Features: rows}
		render(w, r, "mine.html", pd)
	}
}

type featureRowData struct {
	*model.Feature
	CanEditStatus bool
}

type featureDetailData struct {
	Feature       *model.Feature
	Comments      []*model.Comment
	Events        []*model.FeatureEvent
	CanEditStatus bool
	CanRetract    bool
	CanReject     bool
}

func canEditStatus(role string) bool {
	return role == "dev" || role == "admin"
}

// FeatureDetail returns the modal content partial via HTMX GET /features/{id}
func FeatureDetail(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", 400)
			return
		}
		f, err := database.GetFeature(id)
		if err != nil || f == nil {
			http.Error(w, "not found", 404)
			return
		}
		comments, err := database.ListComments(id)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		events, err := database.ListFeatureEvents(id)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		u := UserFromContext(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := PartialTmpl.ExecuteTemplate(w, "feature_detail.html", featureDetailData{
			Feature:       f,
			Comments:      comments,
			Events:        events,
			CanEditStatus: canEditStatus(u.Role),
			CanRetract:    f.Status == "pending" && u.ID == f.CreatedBy,
			CanReject:     canEditStatus(u.Role) && f.Status == "pending" && u.ID != f.CreatedBy,
		}); err != nil {
			http.Error(w, err.Error(), 500)
		}
	}
}

// RetractFeature handles DELETE /features/{id} — creator can retract their own pending feature
func RetractFeature(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", 400)
			return
		}
		if err := database.DeleteFeature(id, u.ID); err != nil {
			http.Error(w, "无法撤回：功能不存在、不属于你或已不是待处理状态", 403)
			return
		}
		hub.Global.Broadcast("feature-list-changed")
		hub.Global.Broadcast("stats-updated")
		w.WriteHeader(http.StatusOK)
	}
}

// AddComment handles POST /features/{id}/comments (HTMX)
func AddComment(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", 400)
			return
		}
		content := strings.TrimSpace(r.FormValue("content"))
		if content == "" {
			http.Error(w, "empty comment", 400)
			return
		}
		c := &model.Comment{FeatureID: id, UserID: u.ID, Content: content}
		if err := database.CreateComment(c); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		comments, err := database.ListComments(id)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		hub.Global.Broadcast("comment-added:" + chi.URLParam(r, "id"))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := PartialTmpl.ExecuteTemplate(w, "comments_partial.html", comments); err != nil {
			http.Error(w, err.Error(), 500)
		}
	}
}

// GetFeatureRow handles GET /features/{id}/row — returns single feature row partial for SSE targeted update.
func GetFeatureRow(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", 400)
			return
		}
		f, err := database.GetFeature(id)
		if err != nil || f == nil {
			http.Error(w, "not found", 404)
			return
		}
		u := UserFromContext(r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		row := featureRowData{Feature: f, CanEditStatus: canEditStatus(u.Role)}
		if err := PartialTmpl.ExecuteTemplate(w, "feature_row.html", row); err != nil {
			http.Error(w, err.Error(), 500)
		}
	}
}

// GetComments handles GET /features/{id}/comments — returns comments partial for SSE targeted update.
func GetComments(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", 400)
			return
		}
		comments, err := database.ListComments(id)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := PartialTmpl.ExecuteTemplate(w, "comments_partial.html", comments); err != nil {
			http.Error(w, err.Error(), 500)
		}
	}
}
