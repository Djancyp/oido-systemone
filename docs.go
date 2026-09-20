package main

import (
	"encoding/json"
	"net/http"

	_ "embed"
)

// TypeSafe's published spec, trimmed to what this server implements.
//
//go:embed openapi.json
var specRaw []byte

// swaggerPage loads Swagger UI from a CDN: dev only, needs internet.
const swaggerPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>oido-systemone API</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui.css"></head>
<body><div id="ui"></div>
<script src="https://cdn.jsdelivr.net/npm/swagger-ui-dist@5/swagger-ui-bundle.js"></script>
<script>SwaggerUIBundle({url: "/openapi.json", dom_id: "#ui", persistAuthorization: true})</script>
</body></html>`

// spec drops paths we do not serve (/v1/models) and points servers at this host.
func spec() ([]byte, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(specRaw, &doc); err != nil {
		return nil, err
	}
	var paths map[string]json.RawMessage
	if err := json.Unmarshal(doc["paths"], &paths); err != nil {
		return nil, err
	}
	delete(paths, "/v1/models")
	doc["paths"], _ = json.Marshal(paths)
	doc["servers"] = json.RawMessage(`[{"url":"/"}]`)
	return json.Marshal(doc)
}

func docsHandlers(mux *http.ServeMux) error {
	b, err := spec()
	if err != nil {
		return err
	}
	mux.HandleFunc("GET /openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(b)
	})
	mux.HandleFunc("GET /docs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(swaggerPage))
	})
	return nil
}
