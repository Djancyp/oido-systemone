// Command oido-systemone serves TypeSafe's System One API (POST /v1/systemone) from a
// local model: MiniCPM5-2B (Q4_K_M) via Kronk. Every answer is one forward pass,
// 1 token, softmax over option-letter logprobs. No text is generated.
// Port of github.com/TheoLeeCJ/Semif webgpu-demo/worker.js directScore().
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ardanlabs/kronk/sdk/kronk"
	"github.com/ardanlabs/kronk/sdk/kronk/model"
	"github.com/ardanlabs/kronk/sdk/tools/libs"
	"github.com/ardanlabs/kronk/sdk/tools/models"
)

// presets are the selectable models (-model / MODEL), each pinned to a revision.
// MiniCPM5-2B is the revision SemIf tested.
var presets = map[string]struct{ id, url string }{
	"minicpm5-2b": {"oido-rlhf-minicpm5-2b", "https://huggingface.co/openbmb/MiniCPM5-2B-GGUF/resolve/2079a22f3beaa4e306449978533478fe0522f4b3/MiniCPM5-2B-Q4_K_M.gguf"},
	"qwen3.5-4b":  {"oido-rlhf-qwen3.5-4b", "https://huggingface.co/unsloth/Qwen3.5-4B-GGUF/resolve/e87f176479d0855a907a41277aca2f8ee7a09523/Qwen3.5-4B-Q4_K_M.gguf"},
	"qwen3-4b":    {"oido-rlhf-qwen3-4b", "https://huggingface.co/unsloth/Qwen3-4B-GGUF/resolve/22c9fc8a8c7700b76a1789366280a6a5a1ad1120/Qwen3-4B-Q4_K_M.gguf"},
}

func presetNames() []string {
	names := make([]string, 0, len(presets))
	for n := range presets {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// modelID is the default model: the first preset loaded. "jev-latest" and "jev-preview"
// are accepted as aliases so TypeSafe SDK defaults work unchanged. Set by run.
var modelID = presets["minicpm5-2b"].id

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %s\n", err)
		os.Exit(1)
	}
}

// initLogging sets the default logger from LOG_LEVEL (debug, info, warn, error;
// default info). Debug also logs prompts and per-pass probabilities, which
// contain request content: for dev only. slog.SetDefault routes the standard
// log package through the same handler.
func initLogging() {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(env("LOG_LEVEL", "info"))); err != nil {
		lvl = slog.LevelInfo
	}
	colorOn = useColor()
	var h slog.Handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl})
	if colorOn {
		h = &colorHandler{mu: &sync.Mutex{}, w: os.Stderr, level: lvl}
	}
	slog.SetDefault(slog.New(h))
}

func run() error {
	initLogging()
	preset := flag.String("model", env("MODEL", "minicpm5-2b,qwen3.5-4b"), "comma-separated model presets to load; the first answers jev-latest/jev-preview")
	addr := flag.String("addr", env("ADDR", ":8080"), "listen address")
	slots := flag.Int("slots", envInt("SLOTS", 1), "parallel slots; >1 re-prefills state per slot, slower on CPU")
	ctxLen := flag.Int("ctx", envInt("CONTEXT_WINDOW", 16384), "context window per slot, tokens")
	queue := flag.Int("queue", envInt("MAX_QUEUE", 64), "max in-flight requests before answering 529")
	flag.Parse()
	nSlots := max(*slots, 1)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	processor, err := initLibs(ctx)
	if err != nil {
		return fmt.Errorf("install: %w", err)
	}
	scorers := map[string]scorer{}
	for i, name := range strings.Split(*preset, ",") {
		m, ok := presets[strings.TrimSpace(name)]
		if !ok {
			return fmt.Errorf("unknown model %q (want one of: %s)", name, strings.Join(presetNames(), ", "))
		}
		if scorers[m.id] != nil {
			continue
		}
		mp, err := download(ctx, m.url)
		if err != nil {
			return fmt.Errorf("download %s: %w", m.id, err)
		}
		krn, err := kronk.New(
			model.WithModelFiles(mp.ModelFiles),
			model.WithContextWindow(*ctxLen),
			model.WithNSeqMax(nSlots),
			model.WithAutoTune(true),
		)
		if err != nil {
			return fmt.Errorf("load %s: %w", m.id, err)
		}
		defer krn.Unload(context.Background())
		scorers[m.id] = kronkScorer(krn, make(chan struct{}, nSlots))
		if env("SELF_CHECK", "true") != "false" {
			if err := selfCheck(ctx, m.id, scorers[m.id]); err != nil {
				return err
			}
		}
		if i == 0 {
			modelID = m.id
		}
	}

	if os.Getenv("API_KEY") == "" {
		slog.Warn("API_KEY not set: /v1/systemone is unauthenticated; keep it on a trusted network")
	}
	a := &api{
		timeout:    envDuration("REQUEST_TIMEOUT", 5*time.Minute),
		met:        newMetrics(),
		score:      scorers[modelID],
		scorers:    scorers,
		key:        os.Getenv("API_KEY"),
		bothOrders: env("BOTH_ORDERS", "true") != "false",
		sem:        make(chan struct{}, max(*queue, 1)),
		maxState:   *ctxLen * bytesPerToken * 3 / 4, // leaves a quarter of the window for questions
	}
	if rps := envFloat("RATE_LIMIT", 0); rps > 0 {
		a.limit = newLimiter(rps, envInt("RATE_BURST", max(int(rps), 1)))
	}
	if env("BANNER", "true") != "false" {
		printBanner(a, *addr, processor)
	}
	return serve(ctx, a, *addr)
}

const logo = `
  mmmm  mmmmm  mmmm    mmmm          mmmm    m               #    "
 m"  "m   #    #   "m m"  "m        #"   " mm#mm  m   m   mmm#  mmm     mmm
 #    #   #    #    # #    #        "#mmm    #    #   #  #" "#    #    #" "#
 #    #   #    #    # #    #            "#   #    #   #  #   #    #    #   #
  #mm#  mm#mm  #mmm"   #mm#         "mmm#"   "mm  "mm"#  "#m##  mm#mm  "#m#"
`

// printBanner writes the logo and where this instance runs to stderr, as one
// block just before the "listening" log line. BANNER=false turns it off.
func printBanner(a *api, addr, processor string) {
	scheme := "http"
	if os.Getenv("TLS_CERT") != "" {
		scheme = "https"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		host, port = "", strings.TrimPrefix(addr, ":")
	}
	name, _ := os.Hostname()
	url := func(h string) string { return fmt.Sprintf("%s://%s:%s", scheme, h, port) }
	bind := url("localhost")
	if host == "" || host == "0.0.0.0" || host == "::" { // all interfaces
		bind += "  ·  " + url(name) + "  (all interfaces)"
	} else if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		bind = url(host)
	}
	ids := []string{modelID + " (default)"}
	for id := range a.scorers {
		if id != modelID {
			ids = append(ids, id)
		}
	}
	auth := paint(green, "bearer key required")
	if a.key == "" {
		auth = paint(yellow, "OFF (no API_KEY)")
	}
	proc := paint(green, processor) // an accelerator
	if processor == "cpu" {
		proc = paint(yellow, processor)
	}
	fmt.Fprintf(os.Stderr, "%s\n  %s  ·  pid %d\n\n", paint(bold+cyan, logo), paint(bold, "System One API, local"), os.Getpid())
	for _, kv := range [][2]string{
		{"listen", paint(cyan, bind)},
		{"docs", paint(cyan, url("localhost")+"/docs")},
		{"models", strings.Join(ids, ", ")},
		{"runs on", fmt.Sprintf("%s  ·  %s  ·  %s/%s  ·  host %s", proc, runtime.Version(), runtime.GOOS, runtime.GOARCH, name)},
		{"auth", auth},
	} {
		if kv[0] == "docs" && env("DOCS", "true") == "false" {
			continue
		}
		fmt.Fprintf(os.Stderr, "  %s %s\n", paint(dim, fmt.Sprintf("%-8s", kv[0])), kv[1])
	}
	fmt.Fprintln(os.Stderr)
}

// statusWriter records the response status for the access log.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (w *statusWriter) WriteHeader(code int) {
	w.code = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// logRequests logs one line per request: no request content, only metadata.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r) // probes are frequent: not logged
			return
		}
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		slog.Info("request", "method", r.Method, "path", r.URL.Path, "status", sw.code,
			"ms", time.Since(start).Milliseconds(), "id", sw.Header().Get("x-typesafe-request-id"), "remote", r.RemoteAddr)
	})
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if n, err := strconv.Atoi(os.Getenv(key)); err == nil {
		return n
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if f, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil && f >= 0 {
		return f
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(key)); err == nil && d >= 0 {
		return d
	}
	return def
}

// routes builds the mux. /healthz is up only once the models are loaded, since
// serve starts after that: it doubles as readiness. DOCS=false hides the Swagger routes.
func routes(a *api) (*http.ServeMux, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/systemone", a.systemOne)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	if a.met != nil {
		mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
			if a.authorized(w, r) {
				a.met.write(w, len(a.sem))
			}
		})
	}
	if env("DOCS", "true") != "false" {
		if err := docsHandlers(mux); err != nil {
			return nil, fmt.Errorf("docs: %w", err)
		}
	}
	return mux, nil
}

func serve(ctx context.Context, a *api, addr string) error {
	mux, err := routes(a)
	if err != nil {
		return err
	}

	// Requests run under reqCtx, not ctx, so a shutdown that outlasts
	// SHUTDOWN_TIMEOUT can cancel the passes still running before models unload.
	reqCtx, cancelReqs := context.WithCancel(context.Background())
	defer cancelReqs()

	// No WriteTimeout: a response legitimately waits behind the model (REQUEST_TIMEOUT bounds it).
	srv := &http.Server{
		Addr:              addr,
		Handler:           logRequests(mux),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       2 * time.Minute,
		BaseContext:       func(net.Listener) context.Context { return reqCtx },
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), envDuration("SHUTDOWN_TIMEOUT", 25*time.Second))
		defer cancel()
		if err := srv.Shutdown(sc); err != nil {
			slog.Warn("shutdown timed out, cancelling in-flight requests", "err", err)
			cancelReqs()
			// Cancelled passes return fast; wait so the model is not unloaded mid-pass.
			for i := 0; i < 100 && len(a.sem) > 0; i++ {
				time.Sleep(50 * time.Millisecond)
			}
			srv.Close()
		}
	}()

	cert, key := os.Getenv("TLS_CERT"), os.Getenv("TLS_KEY")
	slog.Info("listening", "addr", addr, "default", modelID, "models", len(a.scorers), "tls", cert != "", "docs", env("DOCS", "true") != "false")
	if cert != "" {
		err = srv.ListenAndServeTLS(cert, key)
	} else {
		err = srv.ListenAndServe()
	}
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	<-done // in-flight requests drained before the caller unloads the model
	return nil
}

// bytesPerToken is a conservative estimate (measured ~4.1 for English on this model).
// ponytail: byte heuristic; exact fix = tokenize before scoring. errInputTooLong backstops it.
const bytesPerToken = 3

var errInputTooLong = errors.New("input too long for the model context window")

// kronkLog routes Kronk's download/load messages (key[value] pairs) into slog,
// so every line is one format. Progress lines start with '\r' for terminals.
func kronkLog(_ context.Context, msg string, args ...any) {
	if len(args)%2 == 1 {
		args = args[:len(args)-1]
	}
	slog.Info("kronk: "+strings.TrimPrefix(msg, "\r"), args...)
}

// initLibs installs and loads llama.cpp; it returns the backend picked (cpu, vulkan, cuda, ...).
func initLibs(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	l, err := libs.New(libs.WithDetect(ctx, kronkLog), libs.WithValidation(true))
	if err != nil {
		return "", err
	}
	if _, err := l.Download(ctx, kronkLog); err != nil {
		return "", fmt.Errorf("llama.cpp: %w", err)
	}
	return l.Processor(), kronk.Init(kronk.WithLibPath(l.LibsPath()))
}

func download(ctx context.Context, url string) (models.Path, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()

	mdls, err := models.New()
	if err != nil {
		return models.Path{}, err
	}
	return mdls.DownloadURLs(ctx, kronkLog, []string{url}, "", "")
}

// maxLetters is how many options one forward pass can read: labels A-T, and
// Kronk caps top_logprobs at 20.
const maxLetters = 20

// passTimeout bounds one forward pass, after it has a slot.
const passTimeout = 2 * time.Minute

// usage counts tokens across forward passes.
type usage struct{ in, out int }

func (u *usage) add(o usage) { u.in += o.in; u.out += o.out }

// scorer runs one forward pass and returns a probability for each of n option
// letters (A, B, ...). It is the only thing that touches the model.
type scorer func(ctx context.Context, system, user string, n int) ([]float64, usage, error)

// kronkScorer admits at most -slots concurrent forward passes on sem (one per model),
// so time queued behind other requests does not count against passTimeout.
func kronkScorer(krn *kronk.Kronk, sem chan struct{}) scorer {
	return func(ctx context.Context, system, user string, n int) ([]float64, usage, error) {
		select {
		case sem <- struct{}{}:
			defer func() { <-sem }()
		case <-ctx.Done():
			return nil, usage{}, ctx.Err()
		}
		ctx, cancel := context.WithTimeout(ctx, passTimeout)
		defer cancel()

		resp, err := krn.Chat(ctx, model.D{
			"messages": model.DocumentArray(
				model.TextMessage(model.RoleSystem, system),
				model.TextMessage(model.RoleUser, user),
			),
			"max_tokens":      1,
			"temperature":     1.0,
			"top_k":           0,
			"top_p":           1.0,
			"min_p":           0.0,
			"logprobs":        true,
			"top_logprobs":    20,
			"enable_thinking": false, // else model opens with a reasoning block, not a letter
		})
		if ctx.Err() != nil {
			return nil, usage{}, ctx.Err() // Kronk reports a cancelled pass as an empty response, not an error
		}
		if err != nil {
			if errors.Is(err, model.ErrInvalidRequest) || strings.Contains(err.Error(), "exceed context window") {
				return nil, usage{}, fmt.Errorf("%w: %v", errInputTooLong, err)
			}
			return nil, usage{}, err
		}
		if len(resp.Choices) == 0 {
			return nil, usage{}, errors.New("no choices returned")
		}
		var u usage
		if resp.Usage != nil {
			u = usage{in: resp.Usage.PromptTokens, out: resp.Usage.CompletionTokens}
		}
		lp := resp.Choices[0].Logprobs
		if lp == nil || len(lp.Content) == 0 {
			return nil, u, errors.New("no logprobs returned")
		}

		// Kronk has no logit_bias (SemIf uses it to force label tokens into the
		// list), so a letter outside the top-20 gets the 20th logprob: an upper
		// bound, its prob is overstated, never understated.
		// ponytail: approximate tail. Exact fix = read raw logits via yzma directly.
		top := lp.Content[0].TopLogprobs
		floor := math.Inf(1)
		for _, t := range top {
			floor = min(floor, float64(t.Logprob))
		}
		logits := make([]float64, n)
		found := 0
		for i := range logits {
			// "A" and " A" are distinct tokens for the same letter: sum their mass.
			var lps []float64
			for _, t := range top {
				if strings.TrimSpace(t.Token) == letter(i) {
					lps = append(lps, float64(t.Logprob))
				}
			}
			if len(lps) == 0 {
				logits[i] = floor
				continue
			}
			logits[i] = logsumexp(lps)
			found++
		}
		if found == 0 {
			return nil, u, fmt.Errorf("no option letter in top-20 (model first token %q)", lp.Content[0].Token)
		}
		return softmax(logits), u, nil
	}
}

// selfCheck asks a freshly loaded model one question with an obvious answer, in both option
// orders, and fails startup if it errors or picks wrong. It catches a chat template that opens
// with a reasoning block, a missing logprobs path or a bad GGUF before the first request.
// SELF_CHECK=false skips it.
func selfCheck(ctx context.Context, id string, sc scorer) error {
	const want = 2 // "blue"
	start := time.Now()
	p, _, err := (&api{score: sc, bothOrders: true}).pick(ctx,
		"On a clear day the sky is blue.", "What colour is the sky on a clear day?",
		[]string{"red", "green", "blue", "yellow"}, true)
	if err != nil {
		return fmt.Errorf("self-check %s: %w", id, err)
	}
	if !(p[want] >= 0.5) { // chance is 0.25; written so NaN fails too
		return fmt.Errorf("self-check %s: answered %.2f for the obvious option (want >= 0.5), probs %.3f; wrong chat template or model? SELF_CHECK=false skips", id, p[want], p)
	}
	slog.Info("self-check ok", "model", id, "p", fmt.Sprintf("%.3f", p[want]), "ms", time.Since(start).Milliseconds())
	return nil
}

func letter(i int) string { return string(rune('A' + i)) }

func logsumexp(v []float64) float64 {
	m := math.Inf(-1)
	for _, x := range v {
		m = max(m, x)
	}
	var sum float64
	for _, x := range v {
		sum += math.Exp(x - m)
	}
	return m + math.Log(sum)
}

func softmax(v []float64) []float64 {
	m := math.Inf(-1)
	for _, x := range v {
		m = max(m, x)
	}
	var sum float64
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = math.Exp(x - m)
		sum += out[i]
	}
	for i := range out {
		out[i] /= sum
	}
	return out
}
