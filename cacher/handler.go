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

	// Проверка заголовка If-Modified-Since
	ifModifiedSince := r.Header.Get("If-Modified-Since")
	ifModifiedTime, err := time.Parse(time.RFC1123, ifModifiedSince)
	if err == nil {
		c.fiLock.RLock()
		fi, ok := c.info[p]
		c.fiLock.RUnlock()
		if ok && !fi.GetLastModified().After(ifModifiedTime) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
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
	c.fiLock.RLock()
	fi, ok := c.info[p]
	c.fiLock.RUnlock()

	// Устанавливаем заголовок Last-Modified
	if ok && !fi.GetLastModified().IsZero() {
		w.Header().Set("Last-Modified", fi.GetLastModified().Format(time.RFC1123))
	}

	// Определяем Content-Type
	ct := mime.TypeByExtension(path.Ext(p))
	if ct == "" {
		ct = "application/octet-stream"
	}
	w.Header().Set("Content-Type", ct)

	if r.Method == "HEAD" {
		// Для HEAD запроса достаточно установить заголовки и статус
		stat, err := f.Stat()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))
		w.WriteHeader(http.StatusOK)
		return
	}

	// Для GET запроса используем ServeContent
	http.ServeContent(w, r, path.Base(p), fi.GetLastModified(), f)
}
