package main

import (
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// KV is one "Key: Value" line of nordvpn CLI output. Lines indented under a
// key with an empty value (e.g. "Allowlisted subnets:") are collected in List.
type KV struct {
	Key   string   `json:"key"`
	Value string   `json:"value"`
	List  []string `json:"list,omitempty"`
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)

// CleanOutput strips ANSI escapes and the "\r-\r  \r" spinner frames the
// nordvpn CLI writes, keeping each line's leading indentation.
func CleanOutput(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if i := strings.LastIndex(line, "\r"); i >= 0 {
			line = line[i+1:]
		}
		line = strings.TrimRight(line, " \t")
		t := strings.TrimSpace(line)
		if t == "" || t == "-" || t == "\\" || t == "|" || t == "/" {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// ParseKV parses cleaned "Key: Value" output such as `nordvpn status`,
// `nordvpn settings` and `nordvpn account`.
func ParseKV(s string) []KV {
	res := []KV{}
	for _, line := range strings.Split(s, "\n") {
		if line == "" {
			continue
		}
		indented := line[0] == ' ' || line[0] == '\t'
		t := strings.TrimSpace(line)
		if indented && len(res) > 0 && res[len(res)-1].Value == "" {
			last := &res[len(res)-1]
			last.List = append(last.List, t)
			continue
		}
		k, v, ok := strings.Cut(t, ":")
		if !ok {
			continue
		}
		res = append(res, KV{Key: strings.TrimSpace(k), Value: strings.TrimSpace(v)})
	}
	return res
}

// Get returns the value for key, matching case-insensitively.
func Get(kvs []KV, key string) string {
	for _, kv := range kvs {
		if strings.EqualFold(kv.Key, key) {
			return kv.Value
		}
	}
	return ""
}

// LoggedIn reports whether `nordvpn account` output belongs to a logged-in user.
func LoggedIn(accountOut string) bool {
	l := strings.ToLower(accountOut)
	if strings.Contains(l, "not logged in") || strings.Contains(l, "you must log in") {
		return false
	}
	return strings.Contains(l, "email") || strings.Contains(l, "vpn service")
}

var countryRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]+$`)

// ParseCountries turns `nordvpn countries` / `nordvpn groups` output into a sorted list.
func ParseCountries(s string) []string {
	seen := map[string]bool{}
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || unicode.IsSpace(r) })
	res := []string{}
	for _, f := range fields {
		if countryRe.MatchString(f) && !seen[f] {
			seen[f] = true
			res = append(res, f)
		}
	}
	sort.Strings(res)
	return res
}

var targetRe = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// NormalizeTarget validates a connect target (country, city, group or server
// such as "Poland", "United_States", "P2P", "pl123"). Empty means "fastest".
func NormalizeTarget(t string) (string, error) {
	t = strings.Join(strings.Fields(t), "_")
	if t == "" {
		return "", nil
	}
	if !targetRe.MatchString(t) {
		return "", fmt.Errorf("недопустимая локация: используйте буквы, цифры и _")
	}
	return t, nil
}

// ValidateSubnet accepts an IPv4/IPv6 CIDR and returns its canonical form.
func ValidateSubnet(s string) (string, error) {
	_, n, err := net.ParseCIDR(strings.TrimSpace(s))
	if err != nil {
		return "", fmt.Errorf("ожидается подсеть в формате CIDR, например 192.168.1.0/24")
	}
	return n.String(), nil
}

// ValidatePort accepts a single TCP/UDP port number.
func ValidatePort(s string) (string, error) {
	s = strings.TrimSpace(s)
	if i := strings.IndexFunc(s, func(r rune) bool { return !unicode.IsDigit(r) }); i > 0 {
		s = s[:i] // "22 (UDP|TCP)" as shown by `nordvpn settings`
	}
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return "", fmt.Errorf("порт должен быть числом от 1 до 65535")
	}
	return strconv.Itoa(p), nil
}

// FirstLine is used for short error messages in the UI.
func FirstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// Peer is one device from `nordvpn meshnet peer list`.
// Perms holds what THIS Umbrel allows the peer (incoming, routing, local, files)
// and what the peer allows Umbrel (peerIncoming, peerRouting, peerLocal).
type Peer struct {
	Section  string          `json:"section"` // self | local | external
	Hostname string          `json:"hostname"`
	Nickname string          `json:"nickname"`
	IP       string          `json:"ip"`
	Status   string          `json:"status"`
	OS       string          `json:"os"`
	Distro   string          `json:"distro"`
	Perms    map[string]bool `json:"perms"`
}

var peerPermKeys = map[string]string{
	"allow incoming traffic":      "incoming",
	"allow routing":               "routing",
	"allow local network access":  "local",
	"allow sending files":         "files",
	"allows incoming traffic":     "peerIncoming",
	"allows routing":              "peerRouting",
	"allows local network access": "peerLocal",
	"allows sending files":        "peerFiles",
}

// ParsePeers parses cleaned `nordvpn meshnet peer list` output.
func ParsePeers(s string) []Peer {
	res := []Peer{}
	section := ""
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		switch strings.ToLower(strings.TrimSuffix(t, ":")) {
		case "this device":
			section = "self"
			continue
		case "local peers":
			section = "local"
			continue
		case "external peers":
			section = "external"
			continue
		}
		k, v, ok := strings.Cut(t, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if strings.EqualFold(k, "Hostname") {
			res = append(res, Peer{Section: section, Hostname: v, Perms: map[string]bool{}})
			continue
		}
		if len(res) == 0 {
			continue
		}
		p := &res[len(res)-1]
		lk := strings.ToLower(k)
		if perm, ok := peerPermKeys[lk]; ok {
			p.Perms[perm] = strings.EqualFold(v, "enabled")
			continue
		}
		switch lk {
		case "nickname":
			if v != "-" {
				p.Nickname = v
			}
		case "ip":
			p.IP = v
		case "status":
			p.Status = v
		case "os":
			p.OS = v
		case "distribution":
			p.Distro = v
		}
	}
	return res
}

var nickRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,62}$`)
