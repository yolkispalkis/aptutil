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

	// Check If-Modified-Since header
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

	c.fiLock.RLock()
	fi, ok := c.info[p]
	c.fiLock.RUnlock()

	// Set Last-Modified header
	if ok && !fi.GetLastModified().IsZero() {
		w.Header().Set("Last-Modified", fi.GetLastModified().Format(time.RFC1123))
	}

	// Determine Content-Type
	ext := path.Ext(p)
	ct := mime.TypeByExtension(ext)
	w.Header().Set("Content-Type", ct)

	if r.Method == "HEAD" {
		stat, err := f.Stat()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Length", strconv.FormatInt(stat.Size(), 10))
		w.WriteHeader(http.StatusOK)
		return
	}

	http.ServeContent(w, r, path.Base(p), fi.GetLastModified(), f)
}
