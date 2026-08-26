package httpapi

import (
	"embed"
	"log/slog"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed workspace/*
var workspaceFiles embed.FS

var workspaceAssets = map[string]struct {
	file        string
	contentType string
}{
	"":              {file: "workspace/index.html", contentType: "text/html; charset=utf-8"},
	"workspace.css": {file: "workspace/workspace.css", contentType: "text/css; charset=utf-8"},
	"workspace.js":  {file: "workspace/workspace.js", contentType: "text/javascript; charset=utf-8"},
}

func (s *Server) workspaceRoot(response http.ResponseWriter, request *http.Request) {
	http.Redirect(response, request, "/app/", http.StatusTemporaryRedirect)
}

func (s *Server) apiGuide(response http.ResponseWriter, request *http.Request) {
	contents, err := workspaceFiles.ReadFile("workspace/api.html")
	if err != nil {
		slog.Error("load API guide", "error", err)
		writeError(response, http.StatusInternalServerError, "load API guide")
		return
	}
	setWorkspaceHeaders(response)
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	response.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(response, request, "workspace/api.html", time.Time{}, strings.NewReader(string(contents)))
}

func (s *Server) workspace(response http.ResponseWriter, request *http.Request) {
	assetName := strings.TrimPrefix(request.URL.Path, "/app/")
	if assetName != "" && path.Base(assetName) != assetName {
		http.NotFound(response, request)
		return
	}
	asset, exists := workspaceAssets[assetName]
	if !exists {
		http.NotFound(response, request)
		return
	}
	contents, err := workspaceFiles.ReadFile(asset.file)
	if err != nil {
		slog.Error("load workspace asset", "asset", asset.file, "error", err)
		writeError(response, http.StatusInternalServerError, "load workspace")
		return
	}
	setWorkspaceHeaders(response)
	response.Header().Set("Content-Type", asset.contentType)
	response.Header().Set("Cache-Control", "no-cache")
	http.ServeContent(response, request, asset.file, time.Time{}, strings.NewReader(string(contents)))
}

func setWorkspaceHeaders(response http.ResponseWriter) {
	response.Header().Set("Content-Security-Policy", "default-src 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'")
	response.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("X-Content-Type-Options", "nosniff")
}
