package admin

import (
	"embed"
	"net/http"
)

//go:embed assets/*
var webAssets embed.FS

func (h *Handler) registerAssets(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/admin/", http.StatusPermanentRedirect)
	})
	mux.HandleFunc("GET /admin/{$}", serveAsset("assets/index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /admin/assets/styles.css", serveAsset("assets/styles.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /admin/assets/app.js", serveAsset("assets/app.js", "text/javascript; charset=utf-8"))
}

func serveAsset(name, contentType string) http.HandlerFunc {
	contents, err := webAssets.ReadFile(name)
	if err != nil {
		panic(err)
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; "+
				"img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		_, _ = w.Write(contents)
	}
}
