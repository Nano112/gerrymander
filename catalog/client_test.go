package catalog

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientFailsClosedForOutagesAndUnknownSchemas(t *testing.T) {
	for _, tc := range []struct {
		code int
		body string
	}{
		{503, `{"error":"unavailable","message":"try later"}`},
		{200, `{"schema_version":999,"services":[]}`},
		{200, `{"schema_version":1}`},
	} {
		t.Run(tc.body, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("environment") != "production" || r.URL.Query().Get("workspace") != "alice" || r.Header.Get("Authorization") != "Bearer reader" {
					t.Error("reader credential or scope missing")
				}
				w.WriteHeader(tc.code)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			if _, err := NewClient(srv.URL, "reader").Access(t.Context(), "production", "alice"); err == nil {
				t.Fatal("invalid/unavailable registry silently accepted")
			}
		})
	}
}
