package main

// The TypeSafe System One wire contract (https://api.typesafe.ai/openapi.json):
// POST /v1/systemone takes {state, model, questions} and returns
// {model, answers, usage}. Validation errors mimic FastAPI's 422 body.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxBodyBytes = 4 << 20
	maxQuestions = 64  // one goroutine and 2-4 passes each; real Jev is bounded by its token budget instead
	maxOptions   = 255 // Choice, per TypeSafe docs
	groupSize    = 16  // options per stage-1 group when a Choice exceeds maxLetters
	keptGroups   = 3   // stage-2 groups evaluated; the rest share their mass evenly
)

type api struct {
	score      scorer
	key        string        // Bearer key; empty = no auth (internal network)
	bothOrders bool          // average noul/score over forward and reversed option order
	sem        chan struct{} // in-flight request cap; full = 529
	maxState   int
	timeout    time.Duration     // whole-request deadline; 0 = none
	limit      *limiter          // per-client rate limit; nil = off
	met        *metrics          // nil in tests that do not care
	scorers    map[string]scorer // by model id; score is the default, used for the jev-* aliases
}

// resolve maps a request "model" to the id that answers it and its scorer.
func (a *api) resolve(m string) (id string, sc scorer, ok bool) {
	if m == "jev-latest" || m == "jev-preview" || m == modelID {
		return modelID, a.score, true
	}
	sc, ok = a.scorers[m]
	return m, sc, ok
}

// modelNames lists every accepted "model" value.
func (a *api) modelNames() []string {
	names := []string{"jev-latest", "jev-preview", modelID}
	for id := range a.scorers {
		if id != modelID {
			names = append(names, id)
		}
	}
	slices.Sort(names[3:])
	return names
}

// verr is one FastAPI/pydantic-style validation error.
type verr struct {
	Loc  []any          `json:"loc"`
	Msg  string         `json:"msg"`
	Type string         `json:"type"`
	Ctx  map[string]any `json:"ctx,omitempty"`
}

func loc(parts ...any) []any { return parts }

type noulAnswer struct {
	Type string  `json:"type"`
	Noul float64 `json:"noul"`
}

type choiceAnswer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
}

type scoreAnswer struct {
	Type          string                     `json:"type"`
	Score         float64                    `json:"score"`
	Legend        map[string]json.RawMessage `json:"legend"`
	Probabilities map[string]float64         `json:"probabilities"`
	Confidence    float64                    `json:"confidence"`
}

type response struct {
	Model   string         `json:"model"`
	Answers map[string]any `json:"answers"`
	Usage   struct {
		In  int `json:"input_tokens"`
		Out int `json:"output_tokens"`
	} `json:"usage"`
}

// question is a validated request question, criteria rendered to text.
type question struct {
	name, typ, instr string
	names            []string          // choice option names, request order
	descs            []string          // noul: [yes, no]; choice: per option; score: per level
	levels           []json.RawMessage // score: original level JSON, echoed in legend
}

// catch turns a panic into *err, so a crash in one question's goroutine (the
// model runs in cgo) fails that request instead of killing the process.
// It must be deferred directly.
func catch(err *error) {
	if r := recover(); r != nil {
		slog.Error("panic", "value", r, "stack", string(debug.Stack()))
		*err = fmt.Errorf("panic: %v", r)
	}
}

func (a *api) systemOne(w http.ResponseWriter, r *http.Request) {
	b := make([]byte, 16)
	rand.Read(b)
	rid := "req_" + hex.EncodeToString(b)
	w.Header().Set("x-typesafe-request-id", rid)

	sw := &statusWriter{ResponseWriter: w}
	w = sw
	label, start := "none", time.Now() // label: the model that answered, once known
	var tin, tout int
	if a.met != nil {
		defer func() {
			code := sw.code
			if code == 0 {
				code = 499 // client closed before any response
			}
			a.met.observe(label, code, time.Since(start).Milliseconds(), tin, tout)
		}()
	}

	if !a.authorized(w, r) {
		return
	}
	if a.limit != nil && !a.limit.allow(r) {
		w.Header().Set("Retry-After", "1")
		fail(w, http.StatusTooManyRequests, "rate_limit_error", "Too many requests. Please slow down.")
		return
	}
	select {
	case a.sem <- struct{}{}:
		defer func() { <-a.sem }()
	default:
		w.Header().Set("Retry-After", "1")
		fail(w, 529, "overloaded_error", "The server is temporarily overloaded. Please retry shortly.")
		return
	}

	body, err := readAll(w, r)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(w, http.StatusRequestEntityTooLarge, "invalid_request_error", "Request body too large.")
		} else {
			fail(w, http.StatusBadRequest, "invalid_request_error", "Could not read the request body.")
		}
		return
	}
	state, qs, mdl, errs := a.parse(body)
	if len(errs) > 0 {
		slog.Info("validation failed", "id", rid, "errors", len(errs), "first", fmt.Sprintf("%s at %v", errs[0].Type, errs[0].Loc))
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": errs})
		return
	}

	id, sc, _ := a.resolve(mdl)
	label = id
	run := *a // this request's model
	run.score = sc
	ctx := r.Context()
	if a.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.timeout)
		defer cancel()
	}

	answers := make([]any, len(qs))
	uses := make([]usage, len(qs))
	fails := make([]error, len(qs))
	var wg sync.WaitGroup
	for i, q := range qs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer catch(&fails[i])
			answers[i], uses[i], fails[i] = run.answer(ctx, state, q)
		}()
	}
	wg.Wait()

	res := response{Model: id, Answers: make(map[string]any, len(qs))}
	for i, q := range qs {
		if err := fails[i]; err != nil {
			if errors.Is(err, errInputTooLong) {
				writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"detail": []verr{{
					Loc: loc("body", "state"), Msg: "Input is too long for the model context window", Type: "too_long"}}})
				return
			}
			if r.Context().Err() != nil {
				return // client went away
			}
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				slog.Warn("timeout", "id", rid, "question", q.name)
				fail(w, http.StatusGatewayTimeout, "timeout_error", "The request took too long. Try fewer questions or a shorter state.")
				return
			}
			slog.Error("question failed", "id", rid, "question", q.name, "err", err)
			fail(w, http.StatusInternalServerError, "api_error", "Internal error while evaluating the request.")
			return
		}
		slog.Debug("answer", "id", rid, "question", q.name, "type", q.typ, "result", summarize(answers[i]), "in", uses[i].in, "out", uses[i].out)
		res.Answers[q.name] = answers[i]
		res.Usage.In += uses[i].in
		res.Usage.Out += uses[i].out
	}
	tin, tout = res.Usage.In, res.Usage.Out
	writeJSON(w, http.StatusOK, res)
}

// summarize is a one-line view of an answer for debug logs.
func summarize(a any) string {
	switch v := a.(type) {
	case noulAnswer:
		return fmt.Sprintf("noul=%.3f", v.Noul)
	case choiceAnswer:
		return fmt.Sprintf("%s (confidence %.2f)", v.Choice, v.Confidence)
	case scoreAnswer:
		return fmt.Sprintf("score=%.2f (confidence %.2f)", v.Score, v.Confidence)
	}
	return ""
}

// trunc shortens s for debug logs, on a rune boundary.
func trunc(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "..."
	}
	return s
}

// authorized mirrors TypeSafe's auth errors: 403 with no key, 401 with a wrong one.
func (a *api) authorized(w http.ResponseWriter, r *http.Request) bool {
	if a.key == "" {
		return true
	}
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || got == "" {
		fail(w, http.StatusForbidden, "authentication_error", "Must supply an API key! Check your request and try again.")
		return false
	}
	if subtle.ConstantTimeCompare([]byte(got), []byte(a.key)) != 1 {
		fail(w, http.StatusUnauthorized, "authentication_error", "Cannot authenticate with the server. Please check your API key and try again.")
		return false
	}
	return true
}

func fail(w http.ResponseWriter, code int, errType, msg string) {
	writeJSON(w, code, map[string]any{"detail": map[string]string{"error_type": errType, "message": msg}})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("write response", "err", err) // client went away mid-write
	}
}

func readAll(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	var buf bytes.Buffer
	_, err := buf.ReadFrom(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	return buf.Bytes(), err
}

// ---- parsing and validation ----

type kv struct {
	key string
	val json.RawMessage
}

// ordered decodes a JSON object keeping first-seen key order: for a Choice, criteria order
// fixes the option letters the model sees. ok is false when raw is not an object.
func ordered(raw json.RawMessage) (out []kv, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, false
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, false
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, false
		}
		key := t.(string)
		if i := slices.IndexFunc(out, func(e kv) bool { return e.key == key }); i >= 0 {
			out[i].val = v // duplicate key: last value wins, like pydantic
			continue
		}
		out = append(out, kv{key, v})
	}
	return out, true
}

func field(fs []kv, key string) (json.RawMessage, bool) {
	for _, f := range fs {
		if f.key == key {
			return f.val, true
		}
	}
	return nil, false
}

// kind returns the JSON type of raw: string, object, array, null, or other.
func kind(raw json.RawMessage) string {
	switch t := bytes.TrimSpace(raw); {
	case len(t) == 0:
		return "other"
	case t[0] == '"':
		return "string"
	case t[0] == '{':
		return "object"
	case t[0] == '[':
		return "array"
	case string(t) == "null":
		return "null"
	}
	return "other"
}

// text renders string/object/array content for the prompt: strings verbatim,
// structures as indented JSON, null as "".
func text(raw json.RawMessage) string {
	switch kind(raw) {
	case "string":
		var s string
		json.Unmarshal(raw, &s)
		return s
	case "object", "array":
		var buf bytes.Buffer
		if json.Indent(&buf, raw, "", "  ") == nil {
			return buf.String()
		}
	}
	return ""
}

func isContent(raw json.RawMessage) bool { return kind(raw) != "other" && kind(raw) != "null" }

func (a *api) parse(body []byte) (state string, qs []question, mdl string, errs []verr) {
	top, ok := ordered(body)
	if !ok {
		return "", nil, "", []verr{{Loc: loc("body"), Msg: "Input should be a valid dictionary or object to extract fields from", Type: "model_attributes_type"}}
	}

	rawState, has := field(top, "state")
	switch {
	case !has:
		errs = append(errs, verr{Loc: loc("body", "state"), Msg: "Field required", Type: "missing"})
	case !isContent(rawState):
		errs = append(errs, verr{Loc: loc("body", "state"), Msg: "Input should be a valid string, object or array", Type: "string_type"})
	default:
		state = text(rawState)
		if len(state) > a.maxState {
			errs = append(errs, verr{Loc: loc("body", "state"), Msg: fmt.Sprintf("State is too large: %d bytes, max %d (context window)", len(state), a.maxState), Type: "too_long"})
		}
	}

	if rawModel, has := field(top, "model"); !has {
		errs = append(errs, verr{Loc: loc("body", "model"), Msg: "Field required", Type: "missing"})
	} else {
		var m string
		switch err := json.Unmarshal(rawModel, &m); {
		case err != nil:
			errs = append(errs, verr{Loc: loc("body", "model"), Msg: "Input should be a valid string", Type: "string_type"})
		default:
			if _, _, ok := a.resolve(m); !ok {
				errs = append(errs, verr{Loc: loc("body", "model"), Msg: fmt.Sprintf("Unknown model %q. Use one of: %s", m, strings.Join(a.modelNames(), ", ")), Type: "value_error"})
			} else {
				mdl = m
			}
		}
	}

	rawQs, has := field(top, "questions")
	if !has {
		return state, nil, mdl, append(errs, verr{Loc: loc("body", "questions"), Msg: "Field required", Type: "missing"})
	}
	fs, ok := ordered(rawQs)
	if !ok {
		return state, nil, mdl, append(errs, verr{Loc: loc("body", "questions"), Msg: "Input should be a valid dictionary", Type: "dict_type"})
	}
	if len(fs) == 0 {
		return state, nil, mdl, append(errs, verr{Loc: loc("body", "questions"), Msg: "Dictionary should have at least 1 item after validation, not 0", Type: "too_short", Ctx: map[string]any{"field_type": "Dictionary", "min_length": 1, "actual_length": 0}})
	}
	if len(fs) > maxQuestions {
		return state, nil, mdl, append(errs, verr{Loc: loc("body", "questions"), Msg: "At most " + strconv.Itoa(maxQuestions) + " questions per request", Type: "too_long", Ctx: map[string]any{"field_type": "Dictionary", "max_length": maxQuestions, "actual_length": len(fs)}})
	}
	for _, f := range fs {
		q, es := parseQuestion(f.key, f.val)
		errs = append(errs, es...)
		qs = append(qs, q)
	}
	return state, qs, mdl, errs
}

func parseQuestion(name string, raw json.RawMessage) (q question, errs []verr) {
	base := loc("body", "questions", name)
	fs, ok := ordered(raw)
	if !ok {
		return q, []verr{{Loc: base, Msg: "Input should be a valid dictionary or object to extract fields from", Type: "model_attributes_type"}}
	}
	rawType, has := field(fs, "type")
	if !has {
		return q, []verr{{Loc: base, Msg: "Unable to extract tag using discriminator 'type'", Type: "union_tag_not_found", Ctx: map[string]any{"discriminator": "'type'"}}}
	}
	var typ string
	json.Unmarshal(rawType, &typ)
	if !slices.Contains([]string{"noul", "choice", "score"}, typ) {
		return q, []verr{{Loc: base, Msg: fmt.Sprintf("Input tag '%s' found using 'type' does not match any of the expected tags: 'noul', 'choice', 'score'", typ), Type: "union_tag_invalid",
			Ctx: map[string]any{"discriminator": "'type'", "tag": typ, "expected_tags": "'noul', 'choice', 'score'"}}}
	}

	at := append(slices.Clone(base), typ)
	q = question{name: name, typ: typ}
	if v, has := field(fs, "instructions"); has && kind(v) == "other" {
		errs = append(errs, verr{Loc: append(slices.Clone(at), "instructions"), Msg: "Input should be a valid string", Type: "string_type"})
	} else {
		q.instr = text(v)
	}

	crit, hasCrit := field(fs, "criteria")
	critLoc := append(slices.Clone(at), "criteria")
	switch typ {
	case "noul":
		q.descs = []string{"", ""}
		if !hasCrit || kind(crit) == "null" {
			return q, errs
		}
		cs, ok := ordered(crit)
		if !ok {
			return q, append(errs, verr{Loc: critLoc, Msg: "Input should be a valid dictionary or object to extract fields from", Type: "model_attributes_type"})
		}
		for i, k := range []string{"true", "false"} {
			v, _ := field(cs, k)
			if kind(v) == "other" && len(v) > 0 {
				errs = append(errs, verr{Loc: append(slices.Clone(critLoc), k), Msg: "Input should be a valid string", Type: "string_type"})
			}
			q.descs[i] = text(v)
		}
	case "choice":
		if !hasCrit {
			return q, append(errs, verr{Loc: critLoc, Msg: "Field required", Type: "missing"})
		}
		cs, ok := ordered(crit)
		if !ok {
			return q, append(errs, verr{Loc: critLoc, Msg: "Input should be a valid dictionary", Type: "dict_type"})
		}
		if len(cs) == 0 {
			return q, append(errs, verr{Loc: critLoc, Msg: "At least one choice is required", Type: "too_short", Ctx: map[string]any{"min_length": 1}})
		}
		if len(cs) > maxOptions {
			return q, append(errs, verr{Loc: critLoc, Msg: "At most " + strconv.Itoa(maxOptions) + " choices are supported", Type: "too_long", Ctx: map[string]any{"max_length": maxOptions}})
		}
		for _, c := range cs {
			if kind(c.val) == "other" {
				errs = append(errs, verr{Loc: append(slices.Clone(critLoc), c.key), Msg: "Input should be a valid string", Type: "string_type"})
			}
			q.names = append(q.names, c.key)
			q.descs = append(q.descs, text(c.val))
		}
	case "score":
		if !hasCrit {
			return q, append(errs, verr{Loc: critLoc, Msg: "Field required", Type: "missing"})
		}
		var levels []json.RawMessage
		if kind(crit) != "array" || json.Unmarshal(crit, &levels) != nil {
			return q, append(errs, verr{Loc: critLoc, Msg: "Input should be a valid list", Type: "list_type"})
		}
		if len(levels) == 0 {
			return q, append(errs, verr{Loc: critLoc, Msg: "List should have at least 1 item after validation, not 0", Type: "too_short", Ctx: map[string]any{"field_type": "List", "min_length": 1, "actual_length": 0}})
		}
		if len(levels) > maxLetters {
			return q, append(errs, verr{Loc: critLoc, Msg: "At most " + strconv.Itoa(maxLetters) + " score levels are supported", Type: "too_long", Ctx: map[string]any{"max_length": maxLetters}})
		}
		for i, l := range levels {
			if !isContent(l) {
				errs = append(errs, verr{Loc: append(slices.Clone(critLoc), i), Msg: "Input should be a valid string", Type: "string_type"})
			}
			q.descs = append(q.descs, text(l))
		}
		q.levels = levels
	}
	return q, errs
}

// ---- evaluation ----

func (a *api) answer(ctx context.Context, state string, q question) (any, usage, error) {
	switch q.typ {
	case "noul":
		yes, no := "Yes", "No"
		if q.descs[0] != "" {
			yes += ": " + q.descs[0]
		}
		if q.descs[1] != "" {
			no += ": " + q.descs[1]
		}
		instr := q.instr
		if instr == "" {
			instr = "Is the statement about the state true?"
		}
		p, u, err := a.pick(ctx, state, instr, []string{yes, no}, a.bothOrders)
		if err != nil {
			return nil, u, err
		}
		return noulAnswer{"noul", p[0]}, u, nil

	case "choice":
		opts := make([]string, len(q.names))
		for i, n := range q.names {
			opts[i] = n
			if q.descs[i] != "" {
				opts[i] += ": " + q.descs[i]
			}
		}
		p, u, err := a.pickMany(ctx, state, q.instr, opts)
		if err != nil {
			return nil, u, err
		}
		best := 0
		probs := make(map[string]float64, len(p))
		for i, n := range q.names {
			probs[n] = p[i]
			if p[i] > p[best] {
				best = i
			}
		}
		return choiceAnswer{"choice", q.names[best], probs, confidence(p)}, u, nil

	default: // score
		p, u, err := a.pick(ctx, state, q.instr, q.descs, a.bothOrders)
		if err != nil {
			return nil, u, err
		}
		ans := scoreAnswer{Type: "score", Legend: map[string]json.RawMessage{}, Probabilities: map[string]float64{}, Confidence: confidence(p)}
		for i, pi := range p {
			k := strconv.Itoa(i)
			ans.Legend[k] = q.levels[i]
			ans.Probabilities[k] = pi
			ans.Score += float64(i) * pi
		}
		return ans, u, nil
	}
}

// confidence rescales the top probability so a uniform distribution is 0 and a
// certain one is 1. TypeSafe does not publish its formula; this one reproduces
// all 8 probability/confidence pairs in their docs to within 0.01.
func confidence(p []float64) float64 {
	n := float64(len(p))
	if n < 2 {
		return 1
	}
	return (n*slices.Max(p) - 1) / (n - 1)
}

const defaultInstr = "Choose the option that best fits the state."

// prompt builds SemIf's direct-mode prompt with the state in the system message,
// so every question on one state shares a prefilled KV prefix.
func prompt(state, instr string, opts []string) (system, user string) {
	if instr == "" {
		instr = defaultInstr
	}
	lines := make([]string, len(opts))
	labels := make([]string, len(opts))
	for i, o := range opts {
		labels[i] = letter(i)
		lines[i] = labels[i] + ". " + o
	}
	system = "Make the requested decision from the supplied state. Follow the output format exactly.\n\nState:\n" + state
	user = fmt.Sprintf("Question:\n%s\n\nAllowed options:\n%s\n\nReply with exactly one option letter from: %s.",
		instr, strings.Join(lines, "\n"), strings.Join(labels, ", "))
	return system, user
}

// one scores opts (at most maxLetters) in the given order, or reversed to
// cancel position bias, and returns probabilities in the original order.
func (a *api) one(ctx context.Context, state, instr string, opts []string, reversed bool) ([]float64, usage, error) {
	n := len(opts)
	if n == 1 {
		return []float64{1}, usage{}, nil
	}
	order := make([]int, n) // order[i] = original index shown at letter i
	for i := range order {
		order[i] = i
		if reversed {
			order[i] = n - 1 - i
		}
	}
	shown := make([]string, n)
	for i, o := range order {
		shown[i] = opts[o]
	}
	system, user := prompt(state, instr, shown)
	start := time.Now()
	p, u, err := a.score(ctx, system, user, n)
	if err != nil {
		return nil, u, err
	}
	slog.Debug("pass", "options", n, "reversed", reversed, "probs", fmt.Sprintf("%.3f", p), "in", u.in,
		"ms", time.Since(start).Milliseconds(), "system", trunc(system, 300), "user", trunc(user, 500))
	out := make([]float64, n)
	for i, o := range order {
		out[o] = p[i]
	}
	return out, u, nil
}

// pick scores at most maxLetters options; both averages forward and reversed order.
func (a *api) pick(ctx context.Context, state, instr string, opts []string, both bool) ([]float64, usage, error) {
	p, u, err := a.one(ctx, state, instr, opts, false)
	if err != nil || !both || len(opts) < 2 {
		return p, u, err
	}
	r, u2, err := a.one(ctx, state, instr, opts, true)
	u.add(u2)
	if err != nil {
		return nil, u, err
	}
	for i := range p {
		p[i] = (p[i] + r[i]) / 2
	}
	return p, u, nil
}

// pickMany handles Choices past the letter limit in two stages: pick a group of
// options, then pick within the likeliest groups. Probabilities multiply, so
// they still sum to 1.
// ponytail: arbitrary consecutive groups and top-3 pruning; semantic grouping if accuracy needs it.
func (a *api) pickMany(ctx context.Context, state, instr string, opts []string) ([]float64, usage, error) {
	if len(opts) <= maxLetters {
		return a.pick(ctx, state, instr, opts, false)
	}
	ng := (len(opts) + groupSize - 1) / groupSize
	size := (len(opts) + ng - 1) / ng
	var groups [][]string
	descs := []string{}
	for i := 0; i < len(opts); i += size {
		g := opts[i:min(i+size, len(opts))]
		groups = append(groups, g)
		descs = append(descs, "Any of: "+strings.Join(g, "; "))
	}
	pg, u, err := a.pick(ctx, state, instr, descs, false)
	if err != nil {
		return nil, u, err
	}

	idx := make([]int, len(groups))
	for i := range idx {
		idx[i] = i
	}
	slices.SortStableFunc(idx, func(x, y int) int {
		switch {
		case pg[x] > pg[y]:
			return -1
		case pg[x] < pg[y]:
			return 1
		}
		return 0
	})
	kept := idx[:min(keptGroups, len(idx))]

	inner := make([][]float64, len(groups))
	uses := make([]usage, len(groups))
	errs := make([]error, len(groups))
	var wg sync.WaitGroup
	for _, gi := range kept {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer catch(&errs[gi])
			inner[gi], uses[gi], errs[gi] = a.pick(ctx, state, instr, groups[gi], false)
		}()
	}
	wg.Wait()

	out := make([]float64, 0, len(opts))
	for gi, g := range groups {
		u.add(uses[gi])
		if errs[gi] != nil {
			return nil, u, errs[gi]
		}
		for j := range g {
			if inner[gi] != nil {
				out = append(out, pg[gi]*inner[gi][j])
			} else {
				out = append(out, pg[gi]/float64(len(g))) // pruned group: spread its mass evenly
			}
		}
	}
	return out, u, nil
}
