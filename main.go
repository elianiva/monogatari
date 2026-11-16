package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/starfederation/datastar-go/datastar"
)

//go:embed static
var staticFS embed.FS

var room = NewRoom()

// indexHTML is the static shell page, rendered once.
var indexHTML = func() []byte {
	var b bytes.Buffer
	if err := Index().Render(context.Background(), &b); err != nil {
		panic(err)
	}
	return b.Bytes()
}()

// asset is an embedded static file with its ETag and a gzip copy, both built at startup.
type asset struct {
	body, gz []byte
	etag, ct string
}

func loadAssets() map[string]*asset {
	out := map[string]*asset{}
	err := fs.WalkDir(staticFS, "static", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := staticFS.ReadFile(p)
		sum := sha256.Sum256(b)
		a := &asset{body: b, etag: `"` + hex.EncodeToString(sum[:8]) + `"`, ct: mime.TypeByExtension(path.Ext(p))}
		if !strings.HasSuffix(p, ".woff2") {
			var gz bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&gz, gzip.BestCompression)
			_, _ = zw.Write(b) // writes to a bytes.Buffer cannot fail
			_ = zw.Close()
			a.gz = gz.Bytes()
		}
		out["/"+p] = a
		return nil
	})
	if err != nil {
		panic(err) // the embedded files are fixed at build time
	}
	return out
}

// serveStatic answers with an ETag so repeat visits cost a 304, and gzip where it helps.
func serveStatic(assets map[string]*asset) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a := assets[r.URL.Path]
		if a == nil {
			http.NotFound(w, r)
			return
		}
		h := w.Header()
		h.Set("Cache-Control", "no-cache")
		h.Set("ETag", a.etag)
		h.Set("Vary", "Accept-Encoding")
		if r.Header.Get("If-None-Match") == a.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		h.Set("Content-Type", a.ct)
		body := a.body
		if a.gz != nil && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			h.Set("Content-Encoding", "gzip")
			body = a.gz
		}
		h.Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	}
}

// sig holds the signals the browser sends with each request. Pid is the player's private token.
type sig struct {
	Pid  string `json:"pid"`
	Name string `json:"_name"`
	Pass string `json:"_pass"`
}

func readSig(r *http.Request) sig {
	var s sig
	_ = datastar.ReadSignals(r, &s) // bad input leaves s empty, and an empty pid is rejected
	return s
}

func logf(f string, a ...any) {
	log.Printf(f, a...)
	room.addLog(fmt.Sprintf(f, a...))
}

func index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

// clientIP prefers the first X-Forwarded-For entry, which the portal proxy sets.
func clientIP(r *http.Request) string {
	if f := r.Header.Get("X-Forwarded-For"); f != "" {
		return strings.TrimSpace(strings.Split(f, ",")[0])
	}
	h, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return h
}

func validToken(t string) bool { return len(t) >= 16 && len(t) <= 64 }

func stream(w http.ResponseWriter, r *http.Request) {
	tok := readSig(r).Pid
	if !validToken(tok) {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	sse := datastar.NewSSE(w, r, datastar.WithCompression(datastar.WithBrotli(datastar.WithBrotliLevel(4)), datastar.WithGzip()))
	ch := make(chan struct{}, 1)
	room.connect(tok, ch)
	defer room.disconnect(tok, ch) //nolint:contextcheck // the room outlives any one request, so its goroutines must not use the request context
	tick := time.NewTicker(20 * time.Second)
	defer tick.Stop()
	// send patches the page only when this viewer's HTML changed since the last frame.
	var last string
	send := func(force bool) bool { //nolint:contextcheck // rendering only builds a string and needs no cancellation
		v := room.View(tok)
		if v == last && !force {
			return true
		}
		last = v
		return sse.PatchElements(v) == nil
	}
	if !send(true) {
		return
	}
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ch:
			if !send(false) {
				return
			}
		case <-tick.C:
			// A forced frame doubles as a keep-alive: raw writes would bypass the compressor.
			if !send(true) {
				return
			}
		}
	}
}

func act(fn func(tok string, r *http.Request, s sig)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s := readSig(r)
		if !validToken(s.Pid) {
			http.Error(w, "bad id", http.StatusBadRequest)
			return
		}
		fn(s.Pid, r, s)
		datastar.NewSSE(w, r)
	}
}

func qint(r *http.Request, k string) int {
	n, err := strconv.Atoi(r.URL.Query().Get(k))
	if err != nil {
		return -1
	}
	return n
}

func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /static/", serveStatic(loadAssets()))
	mux.HandleFunc("GET /{$}", index)
	mux.HandleFunc("GET /stream", stream)
	post := func(path string, fn func(tok string, r *http.Request, s sig)) { mux.HandleFunc("POST "+path, act(fn)) }
	post("/unlock", func(t string, r *http.Request, s sig) { room.Unlock(t, clientIP(r), s.Pass) })
	post("/join", func(t string, r *http.Request, s sig) { room.Join(t, s.Name) })
	post("/level", func(t string, r *http.Request, s sig) { room.SetLevel(t, qint(r, "lv")) })
	post("/solo", func(t string, r *http.Request, s sig) { room.SetSolo(t, r.URL.Query().Get("on") == "1") })
	post("/ready", func(t string, r *http.Request, s sig) { room.ToggleReady(t) })
	post("/leave", func(t string, r *http.Request, s sig) { room.Leave(t) })
	post("/abort", func(t string, r *http.Request, s sig) { room.Abort(t) })
	post("/kick", func(t string, r *http.Request, s sig) { room.Kick(t, r.URL.Query().Get("id")) })
	post("/brief", func(t string, r *http.Request, s sig) { room.SetStep(t, qint(r, "d")) })
	post("/toggle", func(t string, r *http.Request, s sig) { room.Toggle(t, r.URL.Query().Get("k")) })
	post("/furi", func(t string, r *http.Request, s sig) { room.ToggleFuri(t) })
	post("/startnow", func(t string, r *http.Request, s sig) { room.StartNow(t) })
	post("/skip", func(t string, r *http.Request, s sig) { room.Skip(t) })
	post("/begin", func(t string, r *http.Request, s sig) { room.Begin(t) })
	post("/place", func(t string, r *http.Request, s sig) { room.Place(t, qint(r, "c")) })
	post("/unplace", func(t string, r *http.Request, s sig) { room.Unplace(t, qint(r, "c")) })
	post("/confirm", func(t string, r *http.Request, s sig) { room.Confirm(t) })
	post("/readdone", func(t string, r *http.Request, s sig) { room.ReadDone(t) })
	post("/answer", func(t string, r *http.Request, s sig) { room.Answer(t, qint(r, "o")) })
	post("/next", func(t string, r *http.Request, s sig) { room.Next(t) })
	post("/reset", func(t string, r *http.Request, s sig) { room.Reset() })
	return mux
}

func main() {
	room.Pass = os.Getenv("ROOM_PASSPHRASE")
	if n, err := strconv.Atoi(os.Getenv("MAX_GAMES_PER_HOUR")); err == nil && n > 0 {
		room.MaxGames = n
	}
	port := envOr("PORT", "8080")
	if llm.key == "" {
		log.Printf("no usable LLM key (%s): using the offline mock story", llm.keyEnv)
	} else {
		log.Printf("%s: models %v, judge %v", llm.name, models, judgeOn)
	}
	if room.Pass == "" {
		log.Println("ROOM_PASSPHRASE not set: anyone with the link can join")
	}
	log.Fatal(http.ListenAndServe(":"+port, newMux()))
}
