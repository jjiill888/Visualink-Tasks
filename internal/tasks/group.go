package tasks

import (
	"net/http"
	"strconv"
	"strings"

	"visualink/internal/model"
	"visualink/internal/platform/auth"

	"github.com/go-chi/chi/v5"
)

type groupsData struct {
	Groups []*model.Group
}

func ListGroups(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		groups, err := d.Repo.ListGroups()
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		pd := auth.PageData(r, "groups")
		pd.Data = groupsData{Groups: groups}
		render(w, r, "groups.html", pd)
	}
}

type groupDetailData struct {
	Group     *model.Group
	Features  []featureRowData
	Members   []*model.GroupMember
	SubType   string // current user's subscription type: "" | "member" | "watch"
	AllUsers  []*model.User
	CanManage bool
}

func GroupDetail(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r)
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", 400)
			return
		}
		g, err := d.Repo.GetGroup(id)
		if err != nil || g == nil {
			http.Error(w, "not found", 404)
			return
		}
		features, err := d.Repo.ListFeaturesInGroup(id)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		canEdit := canEditStatus(u.Role)
		rows := make([]featureRowData, len(features))
		for i, f := range features {
			rows[i] = featureRowData{Feature: f, CanEditStatus: canEdit}
		}
		members, _ := d.Repo.ListGroupMembers(id)
		subType, _ := d.Repo.GetGroupSubscription(u.ID, id)
		var allUsers []*model.User
		if u.Role == "admin" {
			allUsers, _ = d.Users.ListAllUsers()
		}
		pd := auth.PageData(r, "groups")
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

func CreateGroup(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r)
		title := strings.TrimSpace(r.FormValue("title"))
		description := strings.TrimSpace(r.FormValue("description"))
		if title == "" {
			http.Redirect(w, r, "/groups?error=title_required", http.StatusSeeOther)
			return
		}
		g := &model.Group{Title: title, Description: description, CreatedBy: u.ID}
		if err := d.Repo.CreateGroup(g); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		http.Redirect(w, r, "/groups", http.StatusSeeOther)
	}
}

// groupActionResponse re-renders the group action button partial.
func groupActionResponse(w http.ResponseWriter, d *Deps, groupID, userID int64) {
	subType, _ := d.Repo.GetGroupSubscription(userID, groupID)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = partials.ExecuteTemplate(w, "group_action_btn.html", map[string]any{
		"GroupID": groupID,
		"SubType": subType,
	})
}

// JoinGroup handles POST /groups/{id}/join — self-join as member
func JoinGroup(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r)
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", 400)
			return
		}
		if err := d.Repo.UpsertGroupSubscription(u.ID, id, "member"); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		groupActionResponse(w, d, id, u.ID)
	}
}

// LeaveGroup handles DELETE /groups/{id}/join — self-leave
func LeaveGroup(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r)
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", 400)
			return
		}
		if err := d.Repo.DeleteGroupSubscription(u.ID, id); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		groupActionResponse(w, d, id, u.ID)
	}
}

// WatchGroup handles POST /groups/{id}/watch — subscribe without joining
func WatchGroup(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r)
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", 400)
			return
		}
		// Only set watch if not already a member
		cur, _ := d.Repo.GetGroupSubscription(u.ID, id)
		if cur != "member" {
			if err := d.Repo.UpsertGroupSubscription(u.ID, id, "watch"); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		groupActionResponse(w, d, id, u.ID)
	}
}

// UnwatchGroup handles DELETE /groups/{id}/watch
func UnwatchGroup(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r)
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
		if err != nil {
			http.Error(w, "invalid id", 400)
			return
		}
		cur, _ := d.Repo.GetGroupSubscription(u.ID, id)
		if cur == "watch" {
			if err := d.Repo.DeleteGroupSubscription(u.ID, id); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
		}
		groupActionResponse(w, d, id, u.ID)
	}
}

// AddGroupMember handles POST /groups/{id}/members — admin adds a user as member
func AddGroupMember(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r)
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
		if err := d.Repo.UpsertGroupSubscription(userID, groupID, "member"); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		members, _ := d.Repo.ListGroupMembers(groupID)
		allUsers, _ := d.Users.ListAllUsers()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = partials.ExecuteTemplate(w, "group_members_partial.html", map[string]any{
			"GroupID":   groupID,
			"Members":   members,
			"AllUsers":  allUsers,
			"CanManage": true,
		})
	}
}

// RemoveGroupMember handles DELETE /groups/{id}/members/{uid} — admin removes a member
func RemoveGroupMember(d *Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := auth.UserFromContext(r)
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
		if err := d.Repo.DeleteGroupSubscription(userID, groupID); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		members, _ := d.Repo.ListGroupMembers(groupID)
		allUsers, _ := d.Users.ListAllUsers()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = partials.ExecuteTemplate(w, "group_members_partial.html", map[string]any{
			"GroupID":   groupID,
			"Members":   members,
			"AllUsers":  allUsers,
			"CanManage": true,
		})
	}
}
