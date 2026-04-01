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
		render(w, r, "groups.html", pd)
	}
}

type groupDetailData struct {
	Group      *model.Group
	Features   []featureRowData
	Members    []*model.GroupMember
	SubType    string // current user's subscription type: "" | "member" | "watch"
	AllUsers   []*model.User
	CanManage  bool
}

func GroupDetail(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
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
		canEdit := canEditStatus(u.Role)
		rows := make([]featureRowData, len(features))
		for i, f := range features {
			rows[i] = featureRowData{Feature: f, CanEditStatus: canEdit}
		}
		members, _ := database.ListGroupMembers(id)
		subType, _ := database.GetGroupSubscription(u.ID, id)
		var allUsers []*model.User
		if u.Role == "admin" {
			allUsers, _ = database.ListAllUsers()
		}
		pd := pageData(r, "groups")
		pd.Data = groupDetailData{
			Group:     g,
			Features:  rows,
			Members:   members,
			SubType:   subType,
			AllUsers:  allUsers,
			CanManage: u.Role == "admin",
		}
		render(w, r, "group_detail.html", pd)
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

// groupActionResponse re-renders the group action button partial.
func groupActionResponse(w http.ResponseWriter, database *db.DB, groupID, userID int64) {
	subType, _ := database.GetGroupSubscription(userID, groupID)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = PartialTmpl.ExecuteTemplate(w, "group_action_btn.html", map[string]any{
		"GroupID": groupID,
		"SubType": subType,
	})
}

// JoinGroup handles POST /groups/{id}/join — self-join as member
func JoinGroup(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", 400)
			return
		}
		if err := database.UpsertGroupSubscription(u.ID, id, "member"); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		groupActionResponse(w, database, id, u.ID)
	}
}

// LeaveGroup handles DELETE /groups/{id}/join — self-leave
func LeaveGroup(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", 400)
			return
		}
		if err := database.DeleteGroupSubscription(u.ID, id); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		groupActionResponse(w, database, id, u.ID)
	}
}

// WatchGroup handles POST /groups/{id}/watch — subscribe without joining
func WatchGroup(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", 400)
			return
		}
		// Only set watch if not already a member
		cur, _ := database.GetGroupSubscription(u.ID, id)
		if cur != "member" {
			if err := database.UpsertGroupSubscription(u.ID, id, "watch"); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		groupActionResponse(w, database, id, u.ID)
	}
}

// UnwatchGroup handles DELETE /groups/{id}/watch
func UnwatchGroup(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", 400)
			return
		}
		cur, _ := database.GetGroupSubscription(u.ID, id)
		if cur == "watch" {
			if err := database.DeleteGroupSubscription(u.ID, id); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		groupActionResponse(w, database, id, u.ID)
	}
}

// AddGroupMember handles POST /groups/{id}/members — admin adds a user as member
func AddGroupMember(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		if u.Role != "admin" {
			http.Error(w, "forbidden", 403)
			return
		}
		groupID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", 400)
			return
		}
		userID, err := strconv.ParseInt(r.FormValue("user_id"), 10, 64)
		if err != nil || userID == 0 {
			http.Error(w, "invalid user_id", 400)
			return
		}
		if err := database.UpsertGroupSubscription(userID, groupID, "member"); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		members, _ := database.ListGroupMembers(groupID)
		allUsers, _ := database.ListAllUsers()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = PartialTmpl.ExecuteTemplate(w, "group_members_partial.html", map[string]any{
			"GroupID":   groupID,
			"Members":   members,
			"AllUsers":  allUsers,
			"CanManage": true,
		})
	}
}

// RemoveGroupMember handles DELETE /groups/{id}/members/{uid} — admin removes a member
func RemoveGroupMember(database *db.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := UserFromContext(r)
		if u.Role != "admin" {
			http.Error(w, "forbidden", 403)
			return
		}
		groupID, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", 400)
			return
		}
		userID, err := strconv.ParseInt(chi.URLParam(r, "uid"), 10, 64)
		if err != nil {
			http.Error(w, "invalid uid", 400)
			return
		}
		if err := database.DeleteGroupSubscription(userID, groupID); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		members, _ := database.ListGroupMembers(groupID)
		allUsers, _ := database.ListAllUsers()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = PartialTmpl.ExecuteTemplate(w, "group_members_partial.html", map[string]any{
			"GroupID":   groupID,
			"Members":   members,
			"AllUsers":  allUsers,
			"CanManage": true,
		})
	}
}
