// Package probe observes a default that depends on the go line of go.mod.
package probe

import (
	"net/http"
	"net/http/httptest"
)

// WildcardStatus registers the pattern "/a/{x}" and requests "/a/b". With
// GODEBUG httpmuxgo121=1, the default for a module that says `go 1.21`, the
// braces are literal and the answer is 404; the 1.22 mux answers 200.
func WildcardStatus() int {
	mux := http.NewServeMux()
	mux.HandleFunc("/a/{x}", func(http.ResponseWriter, *http.Request) {})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/a/b", nil))
	return rec.Code
}
