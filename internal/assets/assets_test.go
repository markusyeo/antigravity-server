package assets

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAppleTouchIconVariants(t *testing.T) {
	h := Handler()

	cases := []struct {
		path     string
		isPublic bool
		status   int
	}{
		{"/apple-touch-icon.png", true, http.StatusOK},
		{"/apple-touch-icon-precomposed.png", true, http.StatusOK},
		{"/apple-touch-icon-120x120.png", true, http.StatusOK},
		{"/apple-touch-icon-180x180.png", true, http.StatusOK},
		{"/apple-touch-icon-240x240.png", true, http.StatusOK},
		{"/apple-touch-icon-240x240-precomposed.png", true, http.StatusOK},
		{"/apple-touch-icon-custom-size.png", true, http.StatusOK},
		{"/favicon.ico", true, http.StatusOK},
		{"/assets/image/antigravity-logo.png", true, http.StatusOK},
		{"/apple-touch-icon.svg", false, http.StatusNotFound},
		{"/other.png", false, http.StatusNotFound},
	}

	for _, tc := range cases {
		if got := IsPublicPath(tc.path); got != tc.isPublic {
			t.Errorf("IsPublicPath(%q) = %v, want %v", tc.path, got, tc.isPublic)
		}

		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		h.ServeHTTP(rec, req)
		if rec.Code != tc.status {
			t.Errorf("GET %q status = %d, want %d", tc.path, rec.Code, tc.status)
		}
		if tc.status == http.StatusOK {
			if ct := rec.Header().Get("Content-Type"); ct == "" {
				t.Errorf("GET %q missing Content-Type", tc.path)
			}
			if len(rec.Body.Bytes()) == 0 {
				t.Errorf("GET %q returned empty body", tc.path)
			}
		}
	}
}
