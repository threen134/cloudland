package apis

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/spf13/viper"
)

func TestIngestBrowserTraces(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotPath, gotType string
	var gotBody []byte
	otlp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotType = r.URL.Path, r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
	}))
	defer otlp.Close()

	post := func(contentType string, body []byte) int {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/telemetry/traces", bytes.NewReader(body))
		c.Request.Header.Set("Content-Type", contentType)
		IngestBrowserTraces(c)
		return w.Code
	}
	defer viper.Set("tracing.browser_otlp_endpoint", "")

	viper.Set("tracing.browser_otlp_endpoint", "")
	if code := post("application/json", []byte(`{}`)); code != http.StatusNoContent {
		t.Errorf("disabled endpoint: got %d, want 204", code)
	}

	viper.Set("tracing.browser_otlp_endpoint", otlp.URL+"/")
	payload := []byte(`{"resourceSpans":[]}`)
	if code := post("application/json", payload); code != http.StatusAccepted {
		t.Errorf("forward: got %d, want 202", code)
	}
	if gotPath != "/v1/traces" || gotType != "application/json" || !bytes.Equal(gotBody, payload) {
		t.Errorf("forwarded path=%q type=%q body=%q", gotPath, gotType, gotBody)
	}
	if code := post("text/plain", payload); code != http.StatusUnsupportedMediaType {
		t.Errorf("wrong content type: got %d, want 415", code)
	}
	if code := post("application/json", bytes.Repeat([]byte("a"), maxTelemetryBody+1)); code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized payload: got %d, want 413", code)
	}
}
