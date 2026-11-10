package httpp

import (
	"net/http"
)

// set the Server header.
type handlerServerHeader struct {
	h http.Handler
}

func (h *handlerServerHeader) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Server", "tdt-media-stream")
	h.h.ServeHTTP(w, r)
}
