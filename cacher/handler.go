package cacher

import (
	"fmt"
	"mime"
	"net/http"
	"path"
	"strconv"
	"time"

	"github.com/cybozu-go/log"
)

type cacheHandler struct {
	*Cacher
}

func (c cacheHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		http.Error(w, "bad method", http.StatusMethodNotAllowed)
		return
	}

	p := path.Clean(r.URL.Path[1:])

	if log.Enabled(log.LvDebug) {
		log.Debug("request path", map[string]interface{}{
			"path": p,
		})
	}

	status, f, err := c.Get(p)
	if err != nil {
		http.Error(w, err.Error(), status)
		return
	}
	if status == http.StatusNotFound {
		http.NotFound(w, r)
		return
	}
	if status != http.StatusOK {
		http.Error(w, fmt.Sprintf("status %d", status), status)
		return
	}
	defer f.Close()

	// Получаем информацию о файле для установки заголовков
	fi, ok := c.info[p]
	stat, err := f.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Проверка заголовка If-Modified-Since
	ifModifiedSince := r.Header.Get("If-Modified-Since")
	ifModifiedTime, err := time.Parse(time.RFC1123, ifModifiedSince)
	if err == nil {
		if ok && !fi.GetLastModified().After(ifModifiedTime) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}

	// Определяем Content-Type
	ct := mime.TypeByExtension(path.Ext(p))

	// Устанавливаем заголовок Last-Modified
	if ok && !fi.GetLastModified().IsZero() {
		w.Header().Set("Last-Modified", fi.GetLastModified().Format(time.RFC1123))
	}

	if r.Method == "HEAD" {
		// Для HEAD запроса достаточно установить заголовки и статус
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct)

		w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))
		w.WriteHeader(http.StatusOK)
		return
	}

	w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}

	// Для GET запроса используем ServeContent
	http.ServeContent(w, r, path.Base(p), fi.GetLastModified(), f)
}
