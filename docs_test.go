package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDocs(t *testing.T) {
	mux := http.NewServeMux()
	if err := docsHandlers(mux); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"/openapi.json": `"/v1/systemone"`, "/docs": "SwaggerUIBundle"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: code %d, missing %s", path, rec.Code, want)
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("GET", "/openapi.json", nil))
	if strings.Contains(rec.Body.String(), `"/v1/models"`) {
		t.Error("spec still lists /v1/models")
	}
}
