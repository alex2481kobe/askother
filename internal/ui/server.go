// Package ui serves AskOther's local, short-lived run viewer.
package ui

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/alex2481kobe/askother/assets"
	"github.com/alex2481kobe/askother/internal/lifecycle"
	"github.com/alex2481kobe/askother/internal/run"
)

//go:embed index.html ui.css ui.js
var files embed.FS

type handler struct {
	d      lifecycle.Deps
	host   string
	origin string
	token  string
}

// Listen opens the UI on a random loopback port. The returned server runs
// only while its caller serves the listener. The URL contains a random token.
func Listen(d lifecycle.Deps) (net.Listener, *http.Server, string, error) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return nil, nil, "", err
	}
	var secret [32]byte
	if _, err := rand.Read(secret[:]); err != nil {
		ln.Close()
		return nil, nil, "", err
	}
	host := ln.Addr().String()
	h := &handler{d: d, host: host, origin: "http://" + host, token: hex.EncodeToString(secret[:])}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
	return ln, srv, h.origin + "/?token=" + h.token, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'")
	if r.Host != h.host || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != h.origin) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/":
		if r.Method != http.MethodGet || !h.allowed(r.URL.Query().Get("token")) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h.serveFile(w, "index.html", "text/html; charset=utf-8")
	case "/ui.css":
		h.serveFile(w, "ui.css", "text/css; charset=utf-8")
	case "/ui.js":
		h.serveFile(w, "ui.js", "text/javascript; charset=utf-8")
	case "/favicon.ico":
		h.serveAsset(w, "favicon-32.png")
	case "/assets/askother.png":
		h.serveAsset(w, "askother-mark.png")
	case "/assets/openai.png":
		h.serveAsset(w, "openai-mark.png")
	case "/assets/claude.png":
		h.serveAsset(w, "claude-mark.png")
	case "/api/runs", "/api/stop", "/api/clear":
		if !h.allowed(r.Header.Get("X-AskOther-Token")) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		h.api(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (h *handler) allowed(got string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(h.token)) == 1
}

func (h *handler) serveFile(w http.ResponseWriter, name, kind string) {
	b, err := files.ReadFile(name)
	if err != nil {
		http.Error(w, "UI file unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", kind)
	_, _ = w.Write(b)
}

func (h *handler) serveAsset(w http.ResponseWriter, name string) {
	b, err := assets.FS.ReadFile(name)
	if err != nil {
		http.Error(w, "asset unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(b)
}

func (h *handler) api(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/runs":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		view, err := h.snapshot()
		if err != nil {
			http.Error(w, "run list unavailable", http.StatusInternalServerError)
			return
		}
		h.json(w, view)
	case "/api/stop", "/api/clear":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var in struct {
			ID       string `json:"id"`
			Finished bool   `json:"finished"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil || dec.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if r.URL.Path == "/api/stop" {
			if in.ID == "" || in.Finished {
				http.Error(w, "run id required", http.StatusBadRequest)
				return
			}
			_, _, err := lifecycle.RequestStop(h.d, in.ID)
			if err != nil {
				http.Error(w, "could not stop run", http.StatusBadRequest)
				return
			}
		} else if err := h.clear(in.ID, in.Finished); err != nil {
			http.Error(w, "could not clear run", http.StatusBadRequest)
			return
		}
		h.json(w, map[string]bool{"ok": true})
	}
}

func (h *handler) clear(id string, finished bool) error {
	if (id == "" && !finished) || (id != "" && finished) {
		return errors.New("choose one clear target")
	}
	if finished {
		runs, err := lifecycle.Status(h.d, "")
		if err != nil {
			return err
		}
		for _, r := range runs {
			if run.IsTerminal(r.State) {
				if err := h.hide(r.ID); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return h.hide(id)
}

func (h *handler) hide(id string) error {
	_, err := h.d.Store.Update(id, func(r *run.Run) error {
		if r.UIHiddenAt == nil {
			now := time.Now().UTC()
			r.UIHiddenAt = &now
		}
		return nil
	})
	return err
}

func (h *handler) json(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
