package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

const statusRaw = "\r-\r  \r\r-\r  \rStatus: Connected\nServer: Poland #123\nHostname: pl123.nordvpn.com\nIP: 1.2.3.4\nCountry: Poland\nCity: Warsaw\nCurrent technology: NORDLYNX\nCurrent protocol: UDP\nTransfer: 1.20 MiB received, 300 KiB sent\nUptime: 5 minutes 3 seconds\n"

const settingsRaw = "\x1b[0mTechnology: NORDLYNX\nFirewall: enabled\nFirewall Mark: 0xe1f1\nRouting: enabled\nAnalytics: disabled\nKill Switch: disabled\nThreat Protection Lite: disabled\nNotify: disabled\nAuto-connect: enabled\nIPv6: disabled\nMeshnet: enabled\nDNS: disabled\nLAN Discovery: enabled\nAllowlisted subnets:\n\t100.64.0.0/10\n\t192.168.50.0/24\nAllowlisted ports:\n\t22 (UDP|TCP)\n"

func TestCleanOutput(t *testing.T) {
	got := CleanOutput(statusRaw)
	if !strings.HasPrefix(got, "Status: Connected\n") {
		t.Fatalf("spinner not stripped: %q", got)
	}
	if strings.Contains(CleanOutput(settingsRaw), "\x1b") {
		t.Fatal("ansi not stripped")
	}
}

func TestParseStatus(t *testing.T) {
	kv := ParseKV(CleanOutput(statusRaw))
	if Get(kv, "status") != "Connected" || Get(kv, "Country") != "Poland" {
		t.Fatalf("bad parse: %+v", kv)
	}
	if Get(kv, "Transfer") != "1.20 MiB received, 300 KiB sent" {
		t.Fatalf("transfer: %q", Get(kv, "Transfer"))
	}
}

func TestParseSettingsLists(t *testing.T) {
	kv := ParseKV(CleanOutput(settingsRaw))
	var subnets, ports []string
	for _, x := range kv {
		switch x.Key {
		case "Allowlisted subnets":
			subnets = x.List
		case "Allowlisted ports":
			ports = x.List
		}
	}
	if !reflect.DeepEqual(subnets, []string{"100.64.0.0/10", "192.168.50.0/24"}) {
		t.Fatalf("subnets: %v", subnets)
	}
	if !reflect.DeepEqual(ports, []string{"22 (UDP|TCP)"}) {
		t.Fatalf("ports: %v", ports)
	}
	if Get(kv, "Meshnet") != "enabled" || Get(kv, "Kill Switch") != "disabled" {
		t.Fatalf("toggles: %+v", kv)
	}
}

func TestLoggedIn(t *testing.T) {
	if LoggedIn("You are not logged in.") {
		t.Fatal("expected logged out")
	}
	if !LoggedIn("Account Information:\nEmail Address: me@example.com\nVPN Service: Active (Expires on Jan 1st, 2027)") {
		t.Fatal("expected logged in")
	}
}

func TestParseCountries(t *testing.T) {
	got := ParseCountries("\r-\r  \rAlbania, Germany\tPoland\nUnited_States, Germany")
	want := []string{"Albania", "Germany", "Poland", "United_States"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestActionValidation(t *testing.T) {
	cases := []struct {
		path string
		req  actionReq
		want []string
		bad  bool
	}{
		{"connect", actionReq{}, []string{"connect"}, false},
		{"connect", actionReq{Target: "united states"}, []string{"connect", "united_states"}, false},
		{"connect", actionReq{Target: "pl; rm -rf /"}, nil, true},
		{"set", actionReq{Name: "killswitch", On: true}, []string{"set", "killswitch", "on"}, false},
		{"set", actionReq{Name: "autoconnect", On: true, Target: "Poland"}, []string{"set", "autoconnect", "on", "Poland"}, false},
		{"set", actionReq{Name: "firewall", On: false}, nil, true},
		{"allowlist", actionReq{Kind: "subnet", Value: "192.168.1.7/24"}, []string{"allowlist", "add", "subnet", "192.168.1.0/24"}, false},
		{"allowlist", actionReq{Kind: "port", Value: "22 (UDP|TCP)", Remove: true}, []string{"allowlist", "remove", "port", "22"}, false},
		{"allowlist", actionReq{Kind: "port", Value: "70000"}, nil, true},
		{"login", actionReq{Token: "abc def"}, nil, true},
		{"login", actionReq{Token: " e9f2abc "}, []string{"login", "--token", "e9f2abc"}, false},
		{"peer", actionReq{Name: "incoming", On: true, Value: "ivan-iphone.nord"}, []string{"meshnet", "peer", "incoming", "allow", "ivan-iphone.nord"}, false},
		{"peer", actionReq{Name: "routing", On: false, Value: "100.70.1.9"}, []string{"meshnet", "peer", "routing", "deny", "100.70.1.9"}, false},
		{"peer", actionReq{Name: "fileshare", On: true, Value: "x"}, nil, true},
		{"peer", actionReq{Name: "local", On: true, Value: "-rf"}, nil, true},
		{"rm", actionReq{}, nil, true},
	}
	for _, c := range cases {
		args, _, err := action(c.path, c.req)
		if c.bad != (err != nil) {
			t.Fatalf("%s %+v: err=%v", c.path, c.req, err)
		}
		if !c.bad && !reflect.DeepEqual(args, c.want) {
			t.Fatalf("%s: got %v want %v", c.path, args, c.want)
		}
	}
	if redact([]string{"login", "--token", "secret"}) != "login --token ***" {
		t.Fatal("token not redacted")
	}
}

type fakeNord struct {
	mu    sync.Mutex
	calls [][]string
	out   map[string]string
}

func (f *fakeNord) Run(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, args)
	f.mu.Unlock()
	key := strings.Join(args, " ")
	if v, ok := f.out[key]; ok {
		return CleanOutput(v), nil
	}
	return "", errors.New("Whoops! Cannot reach System Daemon.")
}

func TestStateAndCSRF(t *testing.T) {
	f := &fakeNord{out: map[string]string{
		"status":            statusRaw,
		"settings":          settingsRaw,
		"account":           "Email Address: me@example.com\nVPN Service: Active",
		"version":           "NordVPN Version 4.1.0",
		"meshnet peer list": "This device:\nHostname: umbrel-xyz.nord\n",
		"connect Germany":   "Connecting to Germany #1\nYou are connected",
	}}
	h := (&Server{nord: f}).routes()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/state", nil))
	var st State
	if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Daemon || !st.Connected || !st.LoggedIn || !strings.Contains(st.Peers, "umbrel-xyz") {
		t.Fatalf("state: %+v", st)
	}

	// No custom header -> rejected, CLI not called.
	before := len(f.calls)
	req := httptest.NewRequest("POST", "/api/action/connect", strings.NewReader(`{"target":"Germany"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || len(f.calls) != before {
		t.Fatalf("csrf guard failed: %d", rec.Code)
	}

	req = httptest.NewRequest("POST", "/api/action/connect", strings.NewReader(`{"target":"Germany"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Requested-With", "nordpanel")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("connect: %d %s", rec.Code, rec.Body)
	}
}

func TestDaemonDown(t *testing.T) {
	h := (&Server{nord: &fakeNord{out: map[string]string{}}}).routes()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/state", nil))
	var st State
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st.Daemon || !strings.Contains(st.Error, "Daemon") {
		t.Fatalf("state: %+v", st)
	}
}
