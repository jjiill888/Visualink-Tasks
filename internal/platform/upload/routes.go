package upload

import "github.com/go-chi/chi/v5"

// Routes 注册通用图片上传/回源路由;挂在登录中间件之内。
func Routes(r chi.Router, store *Store) {
	r.Post("/uploads/image", UploadImage(store))
	r.Get("/uploads/{id}/{variant}", ServeUpload(store))
}
