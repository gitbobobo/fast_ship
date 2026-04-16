package router

import (
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
)

func setupWebRoutes(r *gin.Engine, webDistDir string) {
	if strings.TrimSpace(webDistDir) == "" {
		return
	}

	indexPath := filepath.Join(webDistDir, "index.html")
	if !isFile(indexPath) {
		return
	}

	r.NoRoute(func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.Status(http.StatusNotFound)
			return
		}

		requestPath := path.Clean("/" + strings.TrimPrefix(c.Request.URL.Path, "/"))
		if requestPath == "/api" || strings.HasPrefix(requestPath, "/api/") {
			c.Status(http.StatusNotFound)
			return
		}

		if requestPath != "/" {
			candidate, ok := resolveWebPath(webDistDir, requestPath)
			if !ok {
				c.Status(http.StatusNotFound)
				return
			}
			if isFile(candidate) {
				c.File(candidate)
				return
			}
			if path.Ext(requestPath) != "" {
				c.Status(http.StatusNotFound)
				return
			}
		}

		c.File(indexPath)
	})
}

func resolveWebPath(baseDir, requestPath string) (string, bool) {
	relativePath := strings.TrimPrefix(requestPath, "/")
	relativePath = filepath.FromSlash(relativePath)

	candidate := filepath.Join(baseDir, relativePath)
	rel, err := filepath.Rel(baseDir, candidate)
	if err != nil {
		return "", false
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return candidate, true
}

func isFile(filePath string) bool {
	info, err := os.Stat(filePath)
	if err != nil {
		return false
	}
	return !info.IsDir()
}
