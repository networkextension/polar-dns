package dns

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRegisterWebServesConsoleAndJSON404(t *testing.T) {
	gin.SetMode(gin.TestMode)
	eng := gin.New()
	p := &Plugin{}
	p.registerWeb(eng)

	// "/" → embedded console HTML
	w := httptest.NewRecorder()
	eng.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("/ status=%d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("/ content-type=%q", ct)
	}
	if !strings.Contains(w.Body.String(), "DNS Control Plane") {
		t.Fatal("/ did not serve the console index")
	}

	// unknown non-API path → SPA fallback (html)
	w = httptest.NewRecorder()
	eng.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/anything", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "DNS Control Plane") {
		t.Fatalf("SPA fallback failed: status=%d", w.Code)
	}

	// API/health/metrics paths → JSON 404, not HTML
	for _, pth := range []string{"/api/dns/providers", "/healthz", "/metrics", "/internal/x"} {
		w = httptest.NewRecorder()
		eng.ServeHTTP(w, httptest.NewRequest(http.MethodGet, pth, nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s status=%d want 404", pth, w.Code)
		}
		if strings.Contains(w.Body.String(), "DNS Control Plane") {
			t.Errorf("%s should not serve HTML", pth)
		}
	}
}
