package web

import (
	"net/http"
	"strconv"
	"time"
)

func sourceBlocked(w http.ResponseWriter, wait time.Duration) {
	seconds := int64((wait + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
	http.Error(w, "Too many failed attempts. Please try again later.", http.StatusTooManyRequests)
}
