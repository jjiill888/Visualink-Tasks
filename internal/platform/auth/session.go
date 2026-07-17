// 会话 cookie(原 internal/platform/session 包,随 db 拆分并入 auth——
// 会话的存取本来就依赖 users/sessions 表,分居两包只剩循环依赖风险)。
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"time"

	"visualink/internal/model"
)

const cookieName = "ft_session"
const sessionTTL = 7 * 24 * time.Hour

func newToken() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}

// SetSession 建立会话并下发 cookie。
func SetSession(w http.ResponseWriter, s *Store, userID int64) error {
	token, err := newToken()
	if err != nil {
		return err
	}
	if err := s.CreateSession(token, userID, time.Now().Add(sessionTTL)); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(sessionTTL.Seconds()),
	})
	return nil
}

// SessionUser 从请求 cookie 解出当前用户;未登录返回 (nil, nil)。
func SessionUser(r *http.Request, s *Store) (*model.User, error) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil, nil // 无 cookie = 未登录
	}
	userID, err := s.GetSession(c.Value)
	if err != nil || userID == 0 {
		return nil, err
	}
	return s.GetUserByID(userID)
}

// ClearSession 注销会话并清除 cookie。
func ClearSession(w http.ResponseWriter, r *http.Request, s *Store) {
	c, err := r.Cookie(cookieName)
	if err == nil {
		_ = s.DeleteSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:   cookieName,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
}
