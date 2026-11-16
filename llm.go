package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"text/template"
	"time"
)

//go:embed prompts/*.txt
var promptFS embed.FS

var (
	promptTpl = template.Must(template.ParseFS(promptFS, "prompts/*.txt"))
	llm       = pickAdapter()
	models    = splitModels(os.Getenv("MODELS"), llm.defaultModel)
	httpc     = &http.Client{Timeout: 150 * time.Second, Transport: &http.Transport{
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        32,
		MaxIdleConnsPerHost: 32,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}}
	noFormat  sync.Map
	orPin     = envOr("PROVIDER", "stepfun") // OpenRouter only: pin one upstream provider
	reasoning = envOr("REASONING", "low")    // OpenRouter only: reasoning effort
	judgeOn   = os.Getenv("JUDGE") != "off"
)

// adapter is one LLM backend: where to send the request, how to authenticate, and the request
// fields that only that provider understands. Every backend the app supports speaks the OpenAI
// chat-completions shape, so a new provider is a case in pickAdapter, not a new client.
type adapter struct {
	name         string
	url          string
	key          string
	keyEnv       string
	defaultModel string
	noTemp       bool                      // the model rejects a temperature field
	timeout      time.Duration             // per-call timeout, 0 uses the shared client
	headers      map[string]string         // provider-only request headers
	extra        func(body map[string]any) // provider-only body fields
}

const (
	openrouterURL = "https://openrouter.ai/api/v1/chat/completions"
	geminiURL     = "https://generativelanguage.googleapis.com/v1beta/openai/chat/completions"
	opencodeGoURL = "https://opencode.ai/inference/go/openai/v1/chat/completions"
)

// pickAdapter resolves the backend from the environment. LLM_PROVIDER forces one; otherwise a key
// decides. An empty key means no backend is configured and the app plays the offline mock story.
func pickAdapter() adapter {
	kind := strings.ToLower(os.Getenv("LLM_PROVIDER"))
	orKey, gKey := os.Getenv("OPENROUTER_API_KEY"), envOr("GEMINI_API_KEY", os.Getenv("GOOGLE_API_KEY"))
	ocKey, tok := os.Getenv("OPENCODE_API_KEY"), os.Getenv("AUTH_TOKEN")
	if kind == "" {
		switch {
		case orKey != "":
			kind = "openrouter"
		case gKey != "":
			kind = "gemini"
		case ocKey != "":
			kind = "opencode-go"
		case tok != "":
			kind = "custom"
		}
	}
	switch kind {
	case "openrouter":
		return adapter{
			name: "openrouter", url: openrouterURL, key: orKey, keyEnv: "OPENROUTER_API_KEY",
			defaultModel: "deepseek/deepseek-v4.1-flash-1017",
			extra: func(b map[string]any) {
				if orPin != "" {
					b["provider"] = map[string]any{"order": []string{orPin}, "allow_fallbacks": false}
				}
				if reasoning != "" && reasoning != "off" {
					b["reasoning"] = map[string]string{"effort": reasoning}
				}
			},
		}
	case "gemini":
		return adapter{
			name: "gemini", url: geminiURL, key: gKey, keyEnv: "GEMINI_API_KEY",
			defaultModel: "gemini-3.6-flash,gemini-3.5-flash",
		}
	case "opencode-go":
		// LongCat 2.5 Free rejects temperature, and OpenCode rejects requests without a session id.
		return adapter{
			name: "opencode-go", url: opencodeGoURL, key: ocKey, keyEnv: "OPENCODE_API_KEY",
			defaultModel: "longcat-2.5-free", noTemp: true, timeout: 10 * time.Minute,
			headers: map[string]string{"User-Agent": "monogatari/1.0", "x-opencode-session": newSessionID()},
		}
	case "custom":
		// Any OpenAI-compatible proxy: BASE_URL plus AUTH_TOKEN.
		base := strings.TrimRight(envOr("LLM_BASE_URL", os.Getenv("BASE_URL")), "/")
		if base == "" {
			base = "https://ai.naufal.work"
		}
		if !strings.HasSuffix(base, "/v1") {
			base += "/v1"
		}
		return adapter{
			name: "custom", url: base + "/chat/completions", key: tok, keyEnv: "AUTH_TOKEN",
			defaultModel: "gemini-3.8-flash",
			headers:      map[string]string{"User-Agent": "monogatari/1.0"},
		}
	case "":
		return adapter{keyEnv: "OPENROUTER_API_KEY, GEMINI_API_KEY, OPENCODE_API_KEY or AUTH_TOKEN"}
	}
	return adapter{keyEnv: fmt.Sprintf("unknown LLM_PROVIDER %q, use openrouter, gemini, opencode-go or custom", kind)}
}

// newSessionID is one stable id per process, which OpenCode uses to route requests.
func newSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "monogatari"
	}
	return hex.EncodeToString(b)
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func splitModels(s, def string) []string {
	var out []string
	for _, m := range strings.Split(s, ",") {
		if m = strings.TrimSpace(m); m != "" {
			out = append(out, m)
		}
	}
	if len(out) == 0 && def != "" {
		return splitModels(def, "")
	}
	return out
}

type apiError struct {
	Who    string
	Status int
	Body   string
}

func (a *apiError) Error() string { return fmt.Sprintf("%s %d: %s", a.Who, a.Status, a.Body) }

func (a adapter) request(ctx context.Context, model string, msgs []map[string]string, jsonMode, stream bool) *http.Request {
	body := map[string]any{"model": model, "messages": msgs}
	if !a.noTemp {
		body["temperature"] = 0.8
	}
	if stream {
		body["stream"] = true
	}
	if jsonMode {
		body["response_format"] = map[string]string{"type": "json_object"}
	}
	if a.extra != nil {
		a.extra(body)
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, a.url, bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+a.key)
	req.Header.Set("Content-Type", "application/json")
	if a.name == "openrouter" {
		req.Header.Set("X-Title", "Monogatari")
	}
	for k, v := range a.headers {
		req.Header.Set(k, v)
	}
	return req
}

// hc is the HTTP client for this adapter: a slow backend gets its own timeout.
func (a adapter) hc() *http.Client {
	if a.timeout > 0 {
		return &http.Client{Timeout: a.timeout, Transport: httpc.Transport}
	}
	return httpc
}

func (a adapter) chat(ctx context.Context, model string, msgs []map[string]string, jsonMode bool) (string, error) {
	req := a.request(ctx, model, msgs, jsonMode, false)
	resp, err := a.hc().Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		s := string(data)
		if len(s) > 300 {
			s = s[:300]
		}
		return "", &apiError{a.name, resp.StatusCode, s}
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	if out.Error != nil {
		return "", fmt.Errorf("%s: %s", a.name, out.Error.Message)
	}
	if len(out.Choices) == 0 || strings.TrimSpace(out.Choices[0].Message.Content) == "" {
		return "", fmt.Errorf("empty response")
	}
	return out.Choices[0].Message.Content, nil
}

func (a adapter) chatAuto(ctx context.Context, model string, msgs []map[string]string) (string, error) {
	_, skip := noFormat.Load(model)
	txt, err := a.chat(ctx, model, msgs, !skip)
	if ae := (*apiError)(nil); errors.As(err, &ae) && ae.Status == http.StatusBadRequest && !skip {
		noFormat.Store(model, true)
		return a.chat(ctx, model, msgs, false)
	}
	return txt, err
}

// chatStream is chatAuto for a streamed reply: onDelta gets each piece of text as it arrives.
func (a adapter) chatStream(ctx context.Context, model string, msgs []map[string]string, onDelta func(string)) error {
	_, skip := noFormat.Load(model)
	err := a.streamOnce(ctx, model, msgs, !skip, onDelta)
	if ae := (*apiError)(nil); errors.As(err, &ae) && ae.Status == http.StatusBadRequest && !skip {
		noFormat.Store(model, true)
		return a.streamOnce(ctx, model, msgs, false, onDelta)
	}
	return err
}

func (a adapter) streamOnce(ctx context.Context, model string, msgs []map[string]string, jsonMode bool, onDelta func(string)) error {
	resp, err := a.hc().Do(a.request(ctx, model, msgs, jsonMode, true))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return &apiError{a.name, resp.StatusCode, string(data)}
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	got := false
	for sc.Scan() {
		payload, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok || payload == "[DONE]" {
			continue
		}
		var ev struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(payload), &ev) != nil {
			continue
		}
		if ev.Error != nil {
			return fmt.Errorf("%s: %s", a.name, ev.Error.Message)
		}
		if len(ev.Choices) > 0 && ev.Choices[0].Delta.Content != "" {
			got = true
			onDelta(ev.Choices[0].Delta.Content)
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if !got {
		return fmt.Errorf("empty response")
	}
	return nil
}

// objSplit pulls the objects out of the first JSON array in a stream, one as soon as it closes.
type objSplit struct {
	inArr, inStr, esc bool
	depth             int
	cur               []byte
}

func (o *objSplit) feed(s string) (out [][]byte) {
	for i := range len(s) {
		c := s[i]
		if !o.inArr {
			o.inArr = c == '['
			continue
		}
		if o.depth == 0 && c != '{' {
			continue
		}
		o.cur = append(o.cur, c)
		switch {
		case o.inStr:
			switch {
			case o.esc:
				o.esc = false
			case c == '\\':
				o.esc = true
			case c == '"':
				o.inStr = false
			}
		case c == '"':
			o.inStr = true
		case c == '{':
			o.depth++
		case c == '}':
			if o.depth--; o.depth == 0 {
				out = append(out, o.cur)
				o.cur = nil
			}
		}
	}
	return
}

func extractJSON(s string) string {
	i, j := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if i < 0 || j < i {
		return s
	}
	return s[i : j+1]
}

func generate[T any](ctx context.Context, name, prompt string, check func(*T) error) (*T, error) {
	var feedback string
	var last error
	for attempt := range 4 {
		model := models[attempt%len(models)]
		msgs := []map[string]string{{"role": "user", "content": prompt}}
		if feedback != "" {
			msgs = append(msgs, map[string]string{"role": "user", "content": "Your previous JSON was rejected: " + feedback + ". Return the corrected JSON only."})
		}
		t0 := time.Now()
		txt, err := llm.chatAuto(ctx, model, msgs)
		if err != nil {
			last, feedback = err, ""
			logf("%s attempt %d (%s): %v", name, attempt+1, model, err)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
			}
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(extractJSON(txt)), &v); err != nil {
			last = fmt.Errorf("invalid JSON: %w", err)
			feedback = last.Error()
			logf("%s attempt %d (%s): %v", name, attempt+1, model, last)
			continue
		}
		if err := check(&v); err != nil {
			last, feedback = err, err.Error()
			logf("%s attempt %d (%s): rejected: %v", name, attempt+1, model, err)
			continue
		}
		logf("%s ok via %s in %.1fs (attempt %d)", name, model, time.Since(t0).Seconds(), attempt+1)
		return &v, nil
	}
	return nil, last
}

func render(name string, data any) (string, error) {
	var b bytes.Buffer
	err := promptTpl.ExecuteTemplate(&b, name, data)
	return b.String(), err
}

func genPlan(ctx context.Context, ps []PlayerInfo, nLines int) (*Plan, error) {
	if llm.key == "" {
		time.Sleep(1500 * time.Millisecond)
		return mockPlan(ps, nLines), nil
	}
	prompt, err := render("plan.txt", map[string]any{"Players": ps, "NLines": nLines, "Solo": len(ps) == 1})
	if err != nil {
		return nil, err
	}
	return generate(ctx, "plan", prompt, func(p *Plan) error { return checkPlan(p, len(ps), nLines) })
}

// TLine is a finished line of the story, shown to the model so the next line follows it.
type TLine struct{ Name, JP, EN string }

// lineErr runs the structural checks and requires the chunk count to fit the player's level,
// so one line stays one short sentence.
func lineErr(d *LineData, level string) error {
	if err := checkLine(d); err != nil {
		return err
	}
	rng := chunkRange[level]
	if n := len(d.Chunks); n > rng[1]+1 || n < rng[0]-1 {
		return fmt.Errorf("%d chunks, but a JLPT %s line needs %d to %d. Write one short sentence", n, level, rng[0], rng[1])
	}
	return nil
}

func genLine(ctx context.Context, plan *Plan, ps []PlayerInfo, i int, hist []TLine) (*LineData, error) {
	if llm.key == "" {
		time.Sleep(time.Duration(500+i*400) * time.Millisecond)
		return mockLine(i), nil
	}
	lp := plan.LinePlan[i]
	pl := ps[lp.Player]
	prev, next := "(none, this is the opening)", "(none, this is the ending)"
	if i > 0 {
		prev = plan.LinePlan[i-1].Beat
	}
	if i+1 < len(plan.LinePlan) {
		next = plan.LinePlan[i+1].Beat
	}
	rng := chunkRange[pl.Level]
	prompt, err := render("line.txt", map[string]any{
		"Title": plan.Scenario.Title, "Setting": plan.Scenario.Setting,
		"Solo": len(ps) == 1, "Characters": plan.Characters, "History": hist, "Prev": prev, "Beat": lp.Beat, "Next": next,
		"Speaker": plan.Characters[lp.Player], "Level": pl.Level, "Pos": pl.Pos, "LevelDesc": pl.LevelDesc,
		"Grammar": lp.Grammar, "MinChunks": rng[0], "MaxChunks": rng[1],
	})
	if err != nil {
		return nil, err
	}
	return generate(ctx, fmt.Sprintf("line %d", i), prompt, func(d *LineData) error {
		if err := lineErr(d, pl.Level); err != nil {
			return err
		}
		return judgeLine(ctx, d, pl.Level, len(ps) == 1)
	})
}

// judgeLine asks a second call to review a line that already passed the structural checks.
// It drops accepted orders the reviewer rejects. If the review itself fails, the line is accepted.
func judgeLine(ctx context.Context, d *LineData, level string, solo bool) error {
	if !judgeOn || llm.key == "" {
		return nil
	}
	var orders []string
	for _, o := range d.AcceptedOrders {
		var parts []string
		for _, i := range o {
			parts = append(parts, chunkText(d.Chunks[i]))
		}
		orders = append(orders, strings.Join(parts, " | "))
	}
	prompt, err := render("judge.txt", map[string]any{
		"Level": level, "Sentence": sentence(d, identity(len(d.Chunks))), "English": d.English,
		"Orders": orders, "Quiz": d.Quiz, "Solo": solo,
	})
	if err != nil {
		return nil //nolint:nilerr // a broken review prompt must not reject a line that passed the checks
	}
	txt, err := llm.chatAuto(ctx, models[0], []map[string]string{{"role": "user", "content": prompt}})
	if err != nil {
		logf("judge skipped: %v", err)
		return nil
	}
	var v struct {
		Natural   bool   `json:"natural"`
		AnswerOK  bool   `json:"answer_ok"`
		BadOrders []int  `json:"bad_orders"`
		Narration bool   `json:"narration"`
		Note      string `json:"note"`
	}
	if json.Unmarshal([]byte(extractJSON(txt)), &v) != nil {
		logf("judge skipped: invalid JSON")
		return nil //nolint:nilerr // the review is advisory: if it fails, the line is accepted
	}
	if !v.Natural {
		return fmt.Errorf("a reviewer says the sentence is unnatural or ungrammatical: %s", v.Note)
	}
	if v.Narration && !solo {
		return fmt.Errorf("the line is narration. It must be something the character says aloud to the others")
	}
	if !v.AnswerOK {
		return fmt.Errorf("a reviewer says the quiz answer is wrong or ambiguous: %s", v.Note)
	}
	var keep [][]int
	for i, o := range d.AcceptedOrders {
		if i == 0 || !slices.Contains(v.BadOrders, i) {
			keep = append(keep, o)
		}
	}
	if len(keep) < len(d.AcceptedOrders) {
		logf("judge dropped %d accepted order(s)", len(d.AcceptedOrders)-len(keep))
	}
	d.AcceptedOrders = keep
	return nil
}

type scriptLine struct {
	Index, MinChunks, MaxChunks             int
	Name, Level, Pos, LevelDesc, Beat, Gram string
}

// genScript writes the whole conversation in one streamed call so it reads as one scene. Each line is
// reviewed as soon as it arrives, and lines that are bad or missing are rewritten in parallel with the
// lines before them as context. done is called once per line, with nil if it could not be written.
func genScript(ctx context.Context, plan *Plan, ps []PlayerInfo, done func(i int, d *LineData)) {
	n := len(plan.LinePlan)
	if llm.key == "" {
		for i := range n {
			time.Sleep(400 * time.Millisecond)
			done(i, mockLine(i))
		}
		return
	}
	solo := len(ps) == 1
	var sl []scriptLine
	for i, lp := range plan.LinePlan {
		pl := ps[lp.Player]
		rng := chunkRange[pl.Level]
		sl = append(sl, scriptLine{
			Index: i, MinChunks: rng[0], MaxChunks: rng[1], Name: plan.Characters[lp.Player].Name,
			Level: pl.Level, Pos: pl.Pos, LevelDesc: pl.LevelDesc, Beat: lp.Beat, Gram: lp.Grammar,
		})
	}
	prompt, err := render("script.txt", map[string]any{
		"Title": plan.Scenario.Title, "Setting": plan.Scenario.Setting, "Premise": plan.Scenario.Premise,
		"Characters": plan.Characters, "Lines": sl, "N": n, "Solo": solo,
	})
	if err != nil {
		prompt = ""
	}
	var (
		mu    sync.Mutex
		valid = make([]*LineData, n) // lines that passed the structural checks
		wg    sync.WaitGroup
	)
	settle := func(i int, d *LineData) {
		defer wg.Done()
		level := ps[plan.LinePlan[i].Player].Level
		if d != nil {
			e := judgeLine(ctx, d, level, solo)
			if e == nil {
				done(i, d)
				return
			}
			logf("line %d rejected: %v", i, e)
		}
		var hist []TLine
		mu.Lock()
		for j := range i {
			if v := valid[j]; v != nil {
				hist = append(hist, TLine{Name: plan.Characters[plan.LinePlan[j].Player].Name, JP: sentence(v, identity(len(v.Chunks))), EN: v.English})
			}
		}
		mu.Unlock()
		nd, e := genLine(ctx, plan, ps, i, hist)
		if e != nil {
			logf("line %d failed: %v", i, e)
		}
		done(i, nd)
	}
	seen := 0
	if prompt != "" {
		for attempt := 0; attempt < 2 && seen == 0; attempt++ {
			model := models[attempt%len(models)]
			t0 := time.Now()
			var sp objSplit
			err := llm.chatStream(ctx, model, []map[string]string{{"role": "user", "content": prompt}}, func(delta string) {
				for _, raw := range sp.feed(delta) {
					i := seen
					seen++
					if i >= n {
						continue
					}
					var d LineData
					if json.Unmarshal(raw, &d) != nil {
						logf("line %d rejected: invalid JSON", i)
						wg.Add(1)
						go settle(i, nil)
						continue
					}
					if e := lineErr(&d, ps[plan.LinePlan[i].Player].Level); e != nil {
						logf("line %d rejected: %v", i, e)
						wg.Add(1)
						go settle(i, nil)
						continue
					}
					mu.Lock()
					valid[i] = &d
					mu.Unlock()
					wg.Add(1)
					go settle(i, &d)
				}
			})
			if err != nil {
				logf("script attempt %d (%s): %v", attempt+1, model, err)
			} else {
				logf("script streamed via %s in %.1fs", model, time.Since(t0).Seconds())
			}
		}
	}
	for i := seen; i < n; i++ {
		wg.Add(1)
		go settle(i, nil)
	}
	wg.Wait()
}
