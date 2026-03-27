package handler

import (
	"net/http"
	"strconv"
	"strings"

	"featuretrack/internal/db"
	"featuretrack/internal/model"

	"github.com/go-chi/chi/v5"
)

type groupsData struct {
	Groups []*model.Group
}

func ListGroups(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		groups, err := database.ListGroups()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		pd := pageData(r, "groups")
		pd.Data = groupsData{Groups: groups}
		render(w, "groups.html", pd)
	}
}

type groupDetailData struct {
	Group    *model.Group
	Features []*model.Feature
}

func GroupDetail(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", 400)
			return
		}
		g, err := database.GetGroup(id)
		if err != nil || g == nil {
			http.Error(w, "not found", 404)
			return
		}
		features, err := database.ListFeaturesInGroup(id)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		pd := pageData(r, "groups")
		pd.Data = groupDetailData{Group: g, Features: features}
		render(w, "group_detail.html", pd)
	}
}

func CreateGroup(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		title := strings.TrimSpace(r.FormValue("title"))
		description := strings.TrimSpace(r.FormValue("description"))
		if title == "" {
			http.Redirect(w, r, "/groups?error=title_required", http.StatusSeeOther)
			return
		}
		g := &model.Group{Title: title, Description: description, CreatedBy: u.ID}
		if err := database.CreateGroup(g); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		http.Redirect(w, r, "/groups", http.StatusSeeOther)
	}
}
