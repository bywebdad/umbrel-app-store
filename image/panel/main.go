// nordpanel is a small web panel for the NordVPN Linux daemon running on
// Umbrel. It never talks to NordVPN directly: every action is the official
// `nordvpn` CLI, which reaches nordvpnd through the shared unix socket.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

// Runner executes the nordvpn CLI. Swapped for a fake in tests.
type Runner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

type CLI struct{ Bin string }

func (c CLI) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, c.Bin, args...)
	cmd.Env = append(os.Environ(), "TERM=dumb", "NO_COLOR=1")
	raw, err := cmd.CombinedOutput()
	out := CleanOutput(string(raw))
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out, fmt.Errorf("nordvpn %s: превышено время ожидания", args[0])
	}
	if err != nil {
		if out != "" {
			return out, errors.New(out)
		}
		return out, err
	}
	return out, nil
}

type Server struct {
	nord Runner
	mu   sync.Mutex // serializes state-changing commands
}

type State struct {
	Daemon    bool   `json:"daemon"`
	Error     string `json:"error,omitempty"`
	Version   string `json:"version"`
	LoggedIn  bool   `json:"loggedIn"`
	Account   []KV   `json:"account"`
	Connected bool   `json:"connected"`
	Status    []KV   `json:"status"`
	Settings  []KV   `json:"settings"`
	Peers     []Peer `json:"peers"`
	PeersErr  string `json:"peersError,omitempty"`
}

func (s *Server) run(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.nord.Run(ctx, args...)
}

func (s *Server) state() State {
	st := State{Account: []KV{}, Status: []KV{}, Settings: []KV{}, Peers: []Peer{}}
	status, err := s.run(15*time.Second, "status")
	if err != nil {
		st.Error = FirstLine(err.Error())
		return st
	}
	st.Daemon = true
	st.Status = ParseKV(status)
	st.Connected = strings.EqualFold(Get(st.Status, "Status"), "Connected")

	var wg sync.WaitGroup
	var account, settings, version string
	wg.Add(3)
	go func() { defer wg.Done(); account, _ = s.run(15*time.Second, "account") }()
	go func() { defer wg.Done(); settings, _ = s.run(15*time.Second, "settings") }()
	go func() { defer wg.Done(); version, _ = s.run(10*time.Second, "version") }()
	wg.Wait()

	st.Version = FirstLine(version)
	st.LoggedIn = LoggedIn(account)
	if st.LoggedIn {
		st.Account = ParseKV(account)
	}
	st.Settings = ParseKV(settings)
	if st.LoggedIn && strings.EqualFold(Get(st.Settings, "Meshnet"), "enabled") {
		if peers, err := s.run(20*time.Second, "meshnet", "peer", "list"); err == nil {
			st.Peers = ParsePeers(peers)
		} else {
			st.PeersErr = FirstLine(err.Error())
		}
	}
	return st
}

// toggles maps UI setting names to `nordvpn set <name> on|off`.
var toggles = map[string]bool{
	"killswitch":    true,
	"autoconnect":   true,
	"meshnet":       true,
	"lan-discovery": true,
}

var (
	peerPerms = map[string]bool{"incoming": true, "local": true, "routing": true}
	peerRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

type actionReq struct {
	Token  string `json:"token"`
	Target string `json:"target"`
	Name   string `json:"name"`
	On     bool   `json:"on"`
	Kind   string `json:"kind"`
	Value  string `json:"value"`
	Remove bool   `json:"remove"`
}

// action translates one UI request into nordvpn CLI arguments.
func action(path string, r actionReq) (args []string, timeout time.Duration, err error) {
	timeout = 30 * time.Second
	switch path {
	case "login":
		tok := strings.TrimSpace(r.Token)
		if tok == "" || strings.ContainsAny(tok, " \t\n") {
			return nil, 0, errors.New("вставьте access token из Nord Account")
		}
		return []string{"login", "--token", tok}, 60 * time.Second, nil
	case "logout":
		return []string{"logout"}, timeout, nil
	case "connect":
		t, err := NormalizeTarget(r.Target)
		if err != nil {
			return nil, 0, err
		}
		if t == "" {
			return []string{"connect"}, 90 * time.Second, nil
		}
		return []string{"connect", t}, 90 * time.Second, nil
	case "disconnect":
		return []string{"disconnect"}, timeout, nil
	case "set":
		if !toggles[r.Name] {
			return nil, 0, fmt.Errorf("неизвестная настройка %q", r.Name)
		}
		args = []string{"set", r.Name, onOff(r.On)}
		if r.Name == "autoconnect" && r.On {
			t, err := NormalizeTarget(r.Target)
			if err != nil {
				return nil, 0, err
			}
			if t != "" {
				args = append(args, t)
			}
		}
		if r.Name == "meshnet" {
			timeout = 60 * time.Second
		}
		return args, timeout, nil
	case "allowlist":
		op := "add"
		if r.Remove {
			op = "remove"
		}
		var v string
		switch r.Kind {
		case "subnet":
			v, err = ValidateSubnet(r.Value)
		case "port":
			v, err = ValidatePort(r.Value)
		default:
			err = errors.New("тип исключения: subnet или port")
		}
		if err != nil {
			return nil, 0, err
		}
		return []string{"allowlist", op, r.Kind, v}, timeout, nil
	case "peer":
		// Permissions this Umbrel grants to a Meshnet peer:
		// incoming = peer may connect to Umbrel, local = peer may reach Umbrel's LAN,
		// routing = peer may route its internet traffic through Umbrel.
		if !peerPerms[r.Name] {
			return nil, 0, errors.New("разрешение: incoming, local или routing")
		}
		peer := strings.TrimSpace(r.Value)
		if !peerRe.MatchString(peer) {
			return nil, 0, errors.New("укажите hostname, nickname или IP пира")
		}
		verb := "deny"
		if r.On {
			verb = "allow"
		}
		return []string{"meshnet", "peer", r.Name, verb, peer}, timeout, nil
	case "nickname":
		// Value = peer (empty for this Umbrel), Target = new nickname (empty = remove).
		nick := strings.TrimSpace(r.Target)
		if nick != "" && !nickRe.MatchString(nick) {
			return nil, 0, errors.New("имя: латинские буквы, цифры и дефис, до 63 символов")
		}
		peer := strings.TrimSpace(r.Value)
		if peer == "" {
			if nick == "" {
				return []string{"meshnet", "remove", "nickname"}, timeout, nil
			}
			return []string{"meshnet", "set", "nickname", nick}, timeout, nil
		}
		if !peerRe.MatchString(peer) {
			return nil, 0, errors.New("укажите hostname, nickname или IP пира")
		}
		if nick == "" {
			return []string{"meshnet", "peer", "nickname", "remove", peer}, timeout, nil
		}
		return []string{"meshnet", "peer", "nickname", "set", peer, nick}, timeout, nil
	case "peers-refresh":
		return []string{"meshnet", "peer", "refresh"}, timeout, nil
	}
	return nil, 0, errors.New("неизвестное действие")
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, s.state())
}

func (s *Server) handleCountries(w http.ResponseWriter, r *http.Request) {
	countries, _ := s.run(20*time.Second, "countries")
	groups, _ := s.run(20*time.Second, "groups")
	writeJSON(w, http.StatusOK, map[string][]string{
		"countries": ParseCountries(countries),
		"groups":    ParseCountries(groups),
	})
}

func (s *Server) handleAction(w http.ResponseWriter, r *http.Request) {
	// A custom header plus a JSON body forces a CORS preflight, which this
	// server never answers, so other sites cannot drive the panel (CSRF).
	if r.Method != http.MethodPost ||
		r.Header.Get("X-Requested-With") != "nordpanel" ||
		!strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	var req actionReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/action/")
	args, timeout, err := action(name, req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	log.Printf("nordvpn %s", redact(args))
	out, err := s.run(timeout, args...)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error(), "output": out})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"output": out})
}

func redact(args []string) string {
	c := append([]string(nil), args...)
	for i := range c {
		if i > 0 && c[i-1] == "--token" {
			c[i] = "***"
		}
	}
	return strings.Join(c, " ")
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(static)))
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/countries", s.handleCountries)
	mux.HandleFunc("/api/action/", s.handleAction)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })
	return securityHeaders(mux)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; script-src 'self' 'unsafe-inline'; img-src 'self' data:")
		h.ServeHTTP(w, r)
	})
}

func main() {
	listen := flag.String("listen", "0.0.0.0:8080", "listen address")
	bin := flag.String("nordvpn", "nordvpn", "path to the nordvpn CLI")
	flag.Parse()

	srv := &Server{nord: CLI{Bin: *bin}}
	httpSrv := &http.Server{
		Addr:              *listen,
		Handler:           srv.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      2 * time.Minute,
	}
	log.Printf("nordpanel listening on %s", *listen)
	log.Fatal(httpSrv.ListenAndServe())
}
