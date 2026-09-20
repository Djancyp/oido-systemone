package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// fake favors letter A with 0.7 and spreads the rest evenly; it records prompts.
type fake struct {
	mu      sync.Mutex
	prompts []string
}

func (f *fake) score(_ context.Context, _, user string, n int) ([]float64, usage, error) {
	f.mu.Lock()
	f.prompts = append(f.prompts, user)
	f.mu.Unlock()
	p := make([]float64, n)
	for i := range p {
		p[i] = 0.3 / float64(max(n-1, 1))
	}
	p[0] = 0.7
	return p, usage{in: 10, out: 1}, nil
}

func newTestAPI(f *fake, key string) *api {
	return &api{score: f.score, key: key, bothOrders: true, sem: make(chan struct{}, 4), maxState: 1 << 20}
}

func post(a *api, auth, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/v1/systemone", strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/systemone", a.systemOne)
	mux.ServeHTTP(rec, req)
	return rec
}

// strict decodes with DisallowUnknownFields so an extra or renamed field fails,
// mirroring the OpenAPI response schemas.
func strict(t *testing.T, raw []byte, v any) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
}

func TestContractShapes(t *testing.T) {
	rec := post(newTestAPI(&fake{}, ""), "", `{
	  "state": "Help! My payouts have been failing for 3 days.",
	  "model": "jev-latest",
	  "questions": {
	    "is_urgent": {"type": "noul", "instructions": "Does this convey urgency?", "criteria": {"true": "Explicitly time-sensitive", "false": "No urgency expressed"}},
	    "department": {"type": "choice", "instructions": "Which team?", "criteria": {"billing": "Payments", "technical": null, "sales": "Pricing"}},
	    "frustration": {"type": "score", "instructions": "How frustrated?", "criteria": ["Calm", "Frustrated", "Very angry"]}
	  }}`)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("x-typesafe-request-id"), "req_") {
		t.Fatalf("status %d, id %q, body %s", rec.Code, rec.Header().Get("x-typesafe-request-id"), rec.Body)
	}
	var res struct {
		Model   string                     `json:"model"`
		Answers map[string]json.RawMessage `json:"answers"`
		Usage   struct {
			In  int `json:"input_tokens"`
			Out int `json:"output_tokens"`
		} `json:"usage"`
	}
	strict(t, rec.Body.Bytes(), &res)
	// noul 2 passes + choice 1 + score 2 = 5 passes of (10 in, 1 out)
	if res.Model != modelID || len(res.Answers) != 3 || res.Usage.In != 50 || res.Usage.Out != 5 {
		t.Fatalf("model %q answers %d usage %+v", res.Model, len(res.Answers), res.Usage)
	}

	var noul struct {
		Type string  `json:"type"`
		Noul float64 `json:"noul"`
	}
	strict(t, res.Answers["is_urgent"], &noul)
	if noul.Type != "noul" || math.Abs(noul.Noul-0.5) > 1e-9 { // 0.7 forward, 0.3 reversed
		t.Errorf("noul %+v", noul)
	}

	var ch struct {
		Type          string             `json:"type"`
		Choice        string             `json:"choice"`
		Probabilities map[string]float64 `json:"probabilities"`
		Confidence    float64            `json:"confidence"`
	}
	strict(t, res.Answers["department"], &ch)
	if ch.Choice != "billing" || len(ch.Probabilities) != 3 || math.Abs(ch.Confidence-0.55) > 1e-9 {
		t.Errorf("choice %+v", ch)
	}

	var sc struct {
		Type          string             `json:"type"`
		Score         float64            `json:"score"`
		Legend        map[string]string  `json:"legend"`
		Probabilities map[string]float64 `json:"probabilities"`
		Confidence    float64            `json:"confidence"`
	}
	strict(t, res.Answers["frustration"], &sc)
	if sc.Legend["0"] != "Calm" || sc.Legend["2"] != "Very angry" || len(sc.Probabilities) != 3 {
		t.Errorf("score %+v", sc)
	}
	// forward .7/.15/.15, reversed .15/.15/.7 -> avg .425/.15/.425 -> 0*.425+1*.15+2*.425
	if math.Abs(sc.Score-1.0) > 1e-9 {
		t.Errorf("score value %v", sc.Score)
	}
}

func TestConfidenceMatchesDocs(t *testing.T) {
	for _, c := range []struct {
		p    []float64
		want float64
	}{
		{[]float64{0.02, 0.38, 0.60}, 0.39},
		{[]float64{1, 0, 0, 0, 0}, 1.0},
		{[]float64{0.63, 0.37, 0, 0, 0}, 0.53},
		{[]float64{0.1, 0.37, 0.24, 0.29}, 0.16},
		{[]float64{0.08, 0.92, 0}, 0.88},
		{[]float64{0, 0.76, 0.24}, 0.63},
		{[]float64{0, 0.55, 0.45}, 0.33},
		{[]float64{0, 0.7, 0.3}, 0.54},
	} {
		if got := confidence(c.p); math.Abs(got-c.want) > 0.011 {
			t.Errorf("confidence(%v) = %.3f, docs say %.2f", c.p, got, c.want)
		}
	}
}

func TestValidation(t *testing.T) {
	for _, c := range []struct{ name, body, loc, typ string }{
		{"not object", `[]`, `["body"]`, "model_attributes_type"},
		{"no state", `{"model":"jev-latest","questions":{"q":{"type":"noul"}}}`, `["body","state"]`, "missing"},
		{"null state", `{"state":null,"model":"jev-latest","questions":{"q":{"type":"noul"}}}`, `["body","state"]`, "string_type"},
		{"no model", `{"state":"x","questions":{"q":{"type":"noul"}}}`, `["body","model"]`, "missing"},
		{"bad model", `{"state":"x","model":"gpt","questions":{"q":{"type":"noul"}}}`, `["body","model"]`, "value_error"},
		{"no questions", `{"state":"x","model":"jev-latest"}`, `["body","questions"]`, "missing"},
		{"empty questions", `{"state":"x","model":"jev-latest","questions":{}}`, `["body","questions"]`, "too_short"},
		{"no type", `{"state":"x","model":"jev-latest","questions":{"q":{}}}`, `["body","questions","q"]`, "union_tag_not_found"},
		{"bad type", `{"state":"x","model":"jev-latest","questions":{"q":{"type":"rank"}}}`, `["body","questions","q"]`, "union_tag_invalid"},
		{"choice no criteria", `{"state":"x","model":"jev-latest","questions":{"q":{"type":"choice"}}}`, `["body","questions","q","choice","criteria"]`, "missing"},
		{"score empty", `{"state":"x","model":"jev-latest","questions":{"urgency":{"type":"score","criteria":[]}}}`, `["body","questions","urgency","score","criteria"]`, "too_short"},
		{"score not list", `{"state":"x","model":"jev-latest","questions":{"q":{"type":"score","criteria":{}}}}`, `["body","questions","q","score","criteria"]`, "list_type"},
		{"number instructions", `{"state":"x","model":"jev-latest","questions":{"q":{"type":"noul","instructions":3}}}`, `["body","questions","q","noul","instructions"]`, "string_type"},
	} {
		rec := post(newTestAPI(&fake{}, ""), "", c.body)
		var res struct {
			Detail []struct {
				Loc  json.RawMessage `json:"loc"`
				Type string          `json:"type"`
			} `json:"detail"`
		}
		json.Unmarshal(rec.Body.Bytes(), &res)
		if rec.Code != 422 || len(res.Detail) == 0 || string(res.Detail[0].Loc) != c.loc || res.Detail[0].Type != c.typ {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
	}
}

func TestAuth(t *testing.T) {
	a := newTestAPI(&fake{}, "secret")
	body := `{"state":"x","model":"jev-latest","questions":{"q":{"type":"noul"}}}`
	for _, c := range []struct {
		auth string
		code int
		msg  string
	}{
		{"", 403, "Must supply an API key! Check your request and try again."},
		{"Bearer nope", 401, "Cannot authenticate with the server. Please check your API key and try again."},
		{"Bearer secret", 200, ""},
	} {
		rec := post(a, c.auth, body)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.msg) {
			t.Errorf("auth %q: %d %s", c.auth, rec.Code, rec.Body)
		}
	}
	if !strings.Contains(post(a, "", body).Body.String(), `"error_type":"authentication_error"`) {
		t.Error("auth error body shape")
	}
}

func TestSingleRoute(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/systemone", newTestAPI(&fake{}, "").systemOne)
	for _, c := range [][2]string{{"GET", "/v1/models"}, {"GET", "/healthz"}, {"POST", "/score"}} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(c[0], c[1], nil))
		if rec.Code != 404 {
			t.Errorf("%s %s: %d", c[0], c[1], rec.Code)
		}
	}
}

func TestChoiceKeepsCriteriaOrder(t *testing.T) {
	f := &fake{}
	post(newTestAPI(f, ""), "", `{"state":"x","model":"jev-latest","questions":{"q":{"type":"choice","criteria":{"zebra":"z","apple":"a"}}}}`)
	if len(f.prompts) != 1 || !strings.Contains(f.prompts[0], "A. zebra: z\nB. apple: a") {
		t.Errorf("prompt %q", f.prompts)
	}
}

func TestManyOptionsSumToOne(t *testing.T) {
	var crit []string
	for i := 0; i < 100; i++ {
		crit = append(crit, `"opt`+strings.Repeat("x", i)+`":null`)
	}
	rec := post(newTestAPI(&fake{}, ""), "", `{"state":"x","model":"jev-latest","questions":{"q":{"type":"choice","criteria":{`+strings.Join(crit, ",")+`}}}}`)
	var res struct {
		Answers struct {
			Q struct {
				Probabilities map[string]float64 `json:"probabilities"`
			} `json:"q"`
		} `json:"answers"`
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	var sum float64
	for _, p := range res.Answers.Q.Probabilities {
		sum += p
	}
	if rec.Code != 200 || len(res.Answers.Q.Probabilities) != 100 || math.Abs(sum-1) > 1e-9 {
		t.Errorf("%d options=%d sum=%v", rec.Code, len(res.Answers.Q.Probabilities), sum)
	}
}

func TestOverloaded(t *testing.T) {
	a := newTestAPI(&fake{}, "")
	a.sem = make(chan struct{}, 1)
	a.sem <- struct{}{}
	rec := post(a, "", `{}`)
	if rec.Code != 529 || rec.Header().Get("Retry-After") == "" {
		t.Errorf("%d %v", rec.Code, rec.Header())
	}
}

func TestSoftmaxLogsumexp(t *testing.T) {
	p := softmax([]float64{0, 0, math.Log(2)})
	if math.Abs(p[0]+p[1]+p[2]-1) > 1e-9 || math.Abs(p[2]-0.5) > 1e-9 {
		t.Fatalf("softmax %v", p)
	}
	if got := logsumexp([]float64{math.Log(1), math.Log(3)}); math.Abs(got-math.Log(4)) > 1e-9 {
		t.Fatalf("logsumexp %v", got)
	}
}

// schema compiles a component of TypeSafe's published OpenAPI spec
// (openapi.json, from https://api.typesafe.ai/openapi.json).
func schema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	f, err := os.Open("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("openapi.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("openapi.json#/components/schemas/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func validate(t *testing.T, name string, raw []byte) {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if err := schema(t, name).Validate(inst); err != nil {
		t.Errorf("%s does not match the OpenAPI spec: %v\n%s", name, err, raw)
	}
}

const allTypes = `{
  "state": {"ticket": "I was charged twice."},
  "model": "jev-latest",
  "questions": {
    "billing": {"type": "noul", "instructions": "Is this about billing?"},
    "tone": {"type": "choice", "instructions": "Tone?", "criteria": {"calm": null, "angry": {"what": "hostile"}}},
    "urgency": {"type": "score", "criteria": ["can wait", {"what": "this week"}, ["today"]]}
  }}`

func TestResponsesMatchOpenAPI(t *testing.T) {
	validate(t, "SystemOneRequest", []byte(allTypes)) // the test input itself must be valid
	rec := post(newTestAPI(&fake{}, ""), "", allTypes)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	validate(t, "SystemOneResponse", rec.Body.Bytes())

	rec = post(newTestAPI(&fake{}, ""), "", `{"model":"jev-latest","questions":{"q":{"type":"score","criteria":[]}}}`)
	if rec.Code != 422 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	validate(t, "HTTPValidationError", rec.Body.Bytes())
}

func TestQuestionCap(t *testing.T) {
	var qs []string
	for i := 0; i <= maxQuestions; i++ {
		qs = append(qs, `"q`+strings.Repeat("x", i)+`":{"type":"noul"}`)
	}
	rec := post(newTestAPI(&fake{}, ""), "", `{"state":"x","model":"jev-latest","questions":{`+strings.Join(qs, ",")+`}}`)
	if rec.Code != 422 || !strings.Contains(rec.Body.String(), `"too_long"`) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
}

func TestDuplicateKeysLastWins(t *testing.T) {
	f := &fake{}
	rec := post(newTestAPI(f, ""), "", `{"state":"x","state":"y","model":"jev-latest","questions":{"q":{"type":"noul","instructions":"first"},"q":{"type":"noul","instructions":"second"}}}`)
	var res struct {
		Answers map[string]any `json:"answers"`
	}
	json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != 200 || len(res.Answers) != 1 || len(f.prompts) != 2 || !strings.Contains(f.prompts[0], "second") {
		t.Errorf("%d answers=%d prompts=%q", rec.Code, len(res.Answers), f.prompts)
	}
}

// One access-log line per request, metadata only: request content must appear
// only at debug level.
func TestRequestLogging(t *testing.T) {
	old := slog.Default()
	defer slog.SetDefault(old)
	run := func(level slog.Level) string {
		var buf bytes.Buffer
		slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: level})))
		mux := http.NewServeMux()
		mux.HandleFunc("POST /v1/systemone", newTestAPI(&fake{}, "").systemOne)
		req := httptest.NewRequest("POST", "/v1/systemone", strings.NewReader(
			`{"state":"secret customer text","model":"jev-latest","questions":{"q":{"type":"noul","instructions":"ok?"}}}`))
		logRequests(mux).ServeHTTP(httptest.NewRecorder(), req)
		return buf.String()
	}

	info := run(slog.LevelInfo)
	if !strings.Contains(info, "status=200") || !strings.Contains(info, "id=req_") || strings.Contains(info, "secret customer text") {
		t.Errorf("info log must have status and id and no content:\n%s", info)
	}
	debug := run(slog.LevelDebug)
	if !strings.Contains(debug, "noul=") || !strings.Contains(debug, "secret customer text") {
		t.Errorf("debug log must show answers and prompts:\n%s", debug)
	}
}

// The request "model" picks the scorer; jev-* aliases use the default.
func TestModelSelection(t *testing.T) {
	def, other := &fake{}, &fake{}
	a := newTestAPI(def, "")
	a.scorers = map[string]scorer{modelID: def.score, "oido-rlhf-other": other.score}
	body := func(m string) string {
		return `{"model":"` + m + `","state":"x","questions":{"q":{"type":"noul","instructions":"?"}}}`
	}
	for m, want := range map[string]string{"jev-latest": modelID, modelID: modelID, "oido-rlhf-other": "oido-rlhf-other"} {
		var res response
		rec := post(a, "", body(m))
		strict(t, rec.Body.Bytes(), &res)
		if rec.Code != 200 || res.Model != want {
			t.Errorf("%s: code %d, model %q, want %q", m, rec.Code, res.Model, want)
		}
	}
	if len(other.prompts) == 0 || len(def.prompts) == 0 {
		t.Errorf("both scorers should be hit: default %d, other %d", len(def.prompts), len(other.prompts))
	}
	if rec := post(a, "", body("nope")); rec.Code != 422 || !strings.Contains(rec.Body.String(), "oido-rlhf-other") {
		t.Errorf("unknown model: code %d, body %s", rec.Code, rec.Body)
	}
}

const oneQ = `{"model":"jev-latest","state":"x","questions":{"q":{"type":"noul","instructions":"?"}}}`

// A panic in the model layer fails the request with 500; it must not crash the process.
func TestPanicIsContained(t *testing.T) {
	a := newTestAPI(&fake{}, "")
	a.score = func(context.Context, string, string, int) ([]float64, usage, error) { panic("boom") }
	if rec := post(a, "", oneQ); rec.Code != 500 {
		t.Errorf("code %d, body %s", rec.Code, rec.Body)
	}
}

// A request that outlives REQUEST_TIMEOUT gets 504, not a generic 500.
func TestRequestTimeout(t *testing.T) {
	a := newTestAPI(&fake{}, "")
	a.timeout = 20 * time.Millisecond
	a.score = func(ctx context.Context, _, _ string, _ int) ([]float64, usage, error) {
		<-ctx.Done()
		return nil, usage{}, ctx.Err()
	}
	if rec := post(a, "", oneQ); rec.Code != 504 {
		t.Errorf("code %d, body %s", rec.Code, rec.Body)
	}
}

func TestRoutes(t *testing.T) {
	mux, err := routes(newTestAPI(&fake{}, "secret"))
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec.Code
	}
	if c := get("/healthz"); c != 200 { // no key needed: probes carry none
		t.Errorf("/healthz: %d", c)
	}
	t.Setenv("DOCS", "false")
	mux, _ = routes(newTestAPI(&fake{}, ""))
	if c := get("/docs"); c != 404 {
		t.Errorf("/docs with DOCS=false: %d, want 404", c)
	}
}

// The limiter is per client (API key), and answers 429 once a client's burst is spent.
func TestRateLimit(t *testing.T) {
	a := newTestAPI(&fake{}, "")
	a.limit = newLimiter(0.001, 2) // burst 2, effectively no refill
	code := func(auth string) int { return post(a, auth, oneQ).Code }
	for i := range 2 {
		if c := code("Bearer a"); c != 200 {
			t.Fatalf("request %d of the burst: %d", i+1, c)
		}
	}
	if rec := post(a, "Bearer a", oneQ); rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Errorf("third request: code %d, want 429 + Retry-After", rec.Code)
	}
	if code("Bearer b") != 200 {
		t.Error("another client has its own bucket")
	}
}

func TestMetrics(t *testing.T) {
	a := newTestAPI(&fake{}, "secret")
	a.met = newMetrics()
	post(a, "Bearer secret", oneQ)
	post(a, "Bearer wrong", oneQ)
	mux, _ := routes(a)
	scrape := func(auth string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/metrics", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if c := scrape("").Code; c != 403 {
		t.Errorf("/metrics without key: %d, want 403", c)
	}
	body := scrape("Bearer secret").Body.String()
	for _, want := range []string{
		`oido_requests_total{model="` + modelID + `",status="200"} 1`,
		`oido_requests_total{model="none",status="401"} 1`,
		`oido_tokens_total{model="` + modelID + `",direction="input"} 20`,
		"oido_request_duration_ms_count 2",
		"oido_in_flight 0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}
