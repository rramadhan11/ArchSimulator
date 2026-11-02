package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testDesign = `{"version":"0.1","nodes":[{"id":"c","kind":"client.web"}],"edges":[],"workload":{"entry":"c","targetTps":10}}`

// apiClient sends authenticated same-origin requests to the handler.
type apiClient struct {
	t      *testing.T
	h      http.Handler
	cookie *http.Cookie
}

func newAPIClient(t *testing.T) *apiClient {
	h := mustHandler(t, testConfig(t))
	login := postLogin(h, testUser, testPassword, nil)
	return &apiClient{t: t, h: h, cookie: login.Result().Cookies()[0]}
}

func (c *apiClient) do(method, path, body string, edit ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.AddCookie(c.cookie)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, e := range edit {
		e(req)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	return rec
}

func (c *apiClient) decode(rec *httptest.ResponseRecorder, wantStatus int, v any) {
	c.t.Helper()
	if rec.Code != wantStatus {
		c.t.Fatalf("status %d, want %d: %s", rec.Code, wantStatus, rec.Body)
	}
	if v != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), v); err != nil {
			c.t.Fatalf("decode %s: %v", rec.Body, err)
		}
	}
}

func saveBody(name, note, arch string) string {
	return `{"name":` + jsonString(name) + `,"note":` + jsonString(note) + `,"architecture":` + arch + `}`
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestDesignVersionHistory(t *testing.T) {
	c := newAPIClient(t)
	var m designMeta
	c.decode(c.do("POST", "/api/designs", saveBody("Shop", "first", testDesign)), http.StatusCreated, &m)
	if len(m.ID) != 16 || m.Versions != 1 || m.Name != "Shop" {
		t.Fatalf("created %+v", m)
	}

	// Same content, reformatted: no new version.
	pretty := strings.ReplaceAll(testDesign, ",", ", ")
	c.decode(c.do("POST", "/api/designs/"+m.ID+"/versions", saveBody("Shop", "", pretty)), http.StatusOK, &m)
	if m.Versions != 1 {
		t.Errorf("unchanged save created version %d", m.Versions)
	}
	changed := strings.Replace(testDesign, `"targetTps":10`, `"targetTps":20`, 1)
	c.decode(c.do("POST", "/api/designs/"+m.ID+"/versions", saveBody("Shop v2", "more load", changed)), http.StatusCreated, &m)
	if m.Versions != 2 || m.Name != "Shop v2" {
		t.Errorf("after change: %+v", m)
	}

	var detail struct {
		designMeta
		History []versionInfo `json:"history"`
	}
	c.decode(c.do("GET", "/api/designs/"+m.ID, ""), http.StatusOK, &detail)
	if len(detail.History) != 2 || detail.History[0].Version != 2 || detail.History[1].Note != "first" {
		t.Errorf("history = %+v, want newest first", detail.History)
	}
	var v1 version
	c.decode(c.do("GET", "/api/designs/"+m.ID+"/versions/1", ""), http.StatusOK, &v1)
	if same, _ := equalJSON(v1.Architecture, json.RawMessage(testDesign)); !same || v1.Name != "Shop" {
		t.Errorf("version 1 = %s / %q", v1.Architecture, v1.Name)
	}

	var list []designMeta
	c.decode(c.do("GET", "/api/designs", ""), http.StatusOK, &list)
	if len(list) != 1 || list[0].ID != m.ID {
		t.Errorf("list = %+v", list)
	}
	c.decode(c.do("DELETE", "/api/designs/"+m.ID, ""), http.StatusNoContent, nil)
	c.decode(c.do("GET", "/api/designs/"+m.ID, ""), http.StatusNotFound, nil)
}

func TestDesignListNewestFirst(t *testing.T) {
	c := newAPIClient(t)
	var a, b designMeta
	c.decode(c.do("POST", "/api/designs", saveBody("A", "", testDesign)), http.StatusCreated, &a)
	time.Sleep(5 * time.Millisecond)
	c.decode(c.do("POST", "/api/designs", saveBody("B", "", testDesign)), http.StatusCreated, &b)
	var list []designMeta
	c.decode(c.do("GET", "/api/designs", ""), http.StatusOK, &list)
	if len(list) != 2 || list[0].ID != b.ID {
		t.Errorf("list = %+v, want B first", list)
	}
}

func TestDesignAPIRejectsBadRequests(t *testing.T) {
	c := newAPIClient(t)
	cases := []struct {
		name   string
		rec    *httptest.ResponseRecorder
		status int
	}{
		{"not a design", c.do("POST", "/api/designs", saveBody("x", "", `{"nodes":[]}`)), http.StatusBadRequest},
		{"empty name", c.do("POST", "/api/designs", saveBody("  ", "", testDesign)), http.StatusBadRequest},
		{"unknown field", c.do("POST", "/api/designs", `{"name":"x","architecture":`+testDesign+`,"extra":1}`), http.StatusBadRequest},
		{"too large", c.do("POST", "/api/designs", saveBody("x", "", `{"nodes":[],"edges":[],"workload":{},"pad":"`+strings.Repeat("a", maxDesignBytes)+`"}`)), http.StatusBadRequest},
		{"form post", c.do("POST", "/api/designs", saveBody("x", "", testDesign), func(r *http.Request) {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}), http.StatusUnsupportedMediaType},
		{"cross-origin", c.do("POST", "/api/designs", saveBody("x", "", testDesign), func(r *http.Request) {
			r.Header.Set("Origin", "https://evil.example")
		}), http.StatusForbidden},
		{"cross-origin delete", c.do("DELETE", "/api/designs/0123456789abcdef", "", func(r *http.Request) {
			r.Header.Set("Origin", "https://evil.example")
		}), http.StatusForbidden},
		{"path traversal id", c.do("GET", "/api/designs/..%2f..%2fetc", ""), http.StatusNotFound},
		{"unknown design", c.do("POST", "/api/designs/0123456789abcdef/versions", saveBody("x", "", testDesign)), http.StatusNotFound},
		{"bad version", c.do("GET", "/api/designs/0123456789abcdef/versions/abc", ""), http.StatusNotFound},
		{"unknown api route", c.do("GET", "/api/nope", ""), http.StatusNotFound},
	}
	for _, tc := range cases {
		if tc.rec.Code != tc.status {
			t.Errorf("%s: status %d, want %d (%s)", tc.name, tc.rec.Code, tc.status, tc.rec.Body)
		}
	}
}

func TestDesignAPIRequiresLogin(t *testing.T) {
	h := mustHandler(t, testConfig(t))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/designs", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status %d, want 401", rec.Code)
	}
}
