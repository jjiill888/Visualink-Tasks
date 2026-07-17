package upload

import (
	"visualink/internal/db"

	"github.com/go-chi/chi/v5"
)

// Routes 注册通用图片上传/回源路由;挂在登录中间件之内。
func Routes(r chi.Router, database *db.DB) {
	r.Post("/uploads/image", UploadImage(database))
	r.Get("/uploads/{id}/{variant}", ServeUpload(database))
}
