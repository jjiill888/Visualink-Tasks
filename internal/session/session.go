package session

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"featuretrack/internal/db"
	"featuretrack/internal/model"
)

const cookieName = "ft_session"
const ttl = 7 * 24 * time.Hour

func newToken() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}

func Set(w http.ResponseWriter, r *http.Request, d *db.DB, userID int64) error {
	token, err := newToken()
	if err != nil {
		return err
	}
	if err := d.CreateSession(token, userID, time.Now().Add(ttl)); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(ttl.Seconds()),
	})
	return nil
}

func GetUser(r *http.Request, d *db.DB) (*model.User, error) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil, nil // no cookie = not logged in
	}
	userID, err := d.GetSession(c.Value)
	if err != nil || userID == 0 {
		return nil, err
	}
	return d.GetUserByID(userID)
}

func Delete(w http.ResponseWriter, r *http.Request, d *db.DB) {
	c, err := r.Cookie(cookieName)
	if err == nil {
		_ = d.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:   cookieName,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
}
