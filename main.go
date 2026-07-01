package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Config
// ---------------------------------------------------------------------------

type ServiceConfig struct {
	Port int    `json:"port"`
	URL  string `json:"url"`
}

type Config struct {
	Port     int                      `json:"port"`
	Services map[string]ServiceConfig `json:"services"`
	Title    string                   `json:"title"`
}

// ---------------------------------------------------------------------------
// Shortcuts & settings
// ---------------------------------------------------------------------------

type Shortcut struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Hardcoded bool   `json:"hardcoded,omitempty"`
}

type Settings struct {
	BgColor1 string `json:"bgColor1"`
	BgColor2 string `json:"bgColor2"`
	BgAngle  int    `json:"bgAngle"`
}

// ShortcutsFile is the on-disk format for shortcuts.json.
type ShortcutsFile struct {
	Shortcuts []Shortcut `json:"shortcuts"`
	Order     []string   `json:"order"`
	Settings  Settings   `json:"settings,omitempty"`
}

// ---------------------------------------------------------------------------
// Globals
// ---------------------------------------------------------------------------

var (
	config        Config
	userData      ShortcutsFile
	userDataMu    sync.RWMutex
	shortcutsPath = "shortcuts.json"

	slugRe = regexp.MustCompile(`[^a-z0-9]+`)
)

// ---------------------------------------------------------------------------
// Favicon resolution
// ---------------------------------------------------------------------------

var (
	favCacheDir = "favicon-cache" // set in loadConfig

	// Single HTTP client for all favicon fetching: short timeout and
	// InsecureSkipVerify because homelab services often use self-signed certs.
	favClient = &http.Client{ //nolint:gosec
		Timeout: 7 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			DialContext: (&net.Dialer{
				Timeout: 4 * time.Second,
			}).DialContext,
		},
	}

	// MIME ↔ file-extension tables used by the on-disk favicon cache.
	mimeToExt = map[string]string{
		"image/png":                "png",
		"image/jpeg":               "jpg",
		"image/gif":                "gif",
		"image/svg+xml":            "svg",
		"image/webp":               "webp",
		"image/x-icon":             "ico",
		"image/vnd.microsoft.icon": "ico",
	}
	extToMIME = map[string]string{
		"png":  "image/png",
		"jpg":  "image/jpeg",
		"gif":  "image/gif",
		"svg":  "image/svg+xml",
		"webp": "image/webp",
		"ico":  "image/x-icon",
	}

	// Regexes for parsing <link> tags in HTML.
	reLinkTag  = regexp.MustCompile(`(?i)<link\b([^>]*)>`)
	reAttrRel  = regexp.MustCompile(`(?i)\brel\s*=\s*(?:"([^"]*)"|'([^']*)'|(\S+?)[>\s/])`)
	reAttrHref = regexp.MustCompile(`(?i)\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|(\S+?)[>\s/])`)
	reAttrSize = regexp.MustCompile(`(?i)\bsizes\s*=\s*(?:"([^"]*)"|'([^']*)'|(\S+?)[>\s/])`)
)

// isLocalHostname returns true for localhost, loopback, RFC-1918, and common
// local TLDs — domains Google's CDN cannot reach.
func isLocalHostname(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" || host == "::1" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified()
	}
	return strings.HasSuffix(host, ".local") ||
		strings.HasSuffix(host, ".lan") ||
		strings.HasSuffix(host, ".internal") ||
		strings.HasSuffix(host, ".home.arpa")
}

// fetchImage GETs a URL and returns the body only if it looks like an image.
func fetchImage(rawURL string) ([]byte, string) {
	resp, err := favClient.Get(rawURL)
	if err != nil {
		return nil, ""
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, ""
	}

	ct := strings.TrimSpace(strings.ToLower(strings.Split(resp.Header.Get("Content-Type"), ";")[0]))

	// Accept known image MIME types; also accept octet-stream for servers
	// that don't set Content-Type correctly for favicon.ico.
	if !strings.HasPrefix(ct, "image/") && ct != "application/octet-stream" {
		return nil, ""
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil || len(data) < 8 {
		return nil, ""
	}

	// For octet-stream, detect the real MIME type from magic bytes.
	if ct == "application/octet-stream" {
		ct = detectImageMIME(data)
		if ct == "" {
			return nil, ""
		}
	}
	return data, ct
}

// detectImageMIME sniffs common image formats from the first few bytes.
func detectImageMIME(b []byte) string {
	if len(b) < 4 {
		return ""
	}
	switch {
	case b[0] == 0x89 && b[1] == 0x50 && b[2] == 0x4E:
		return "image/png"
	case b[0] == 0xFF && b[1] == 0xD8:
		return "image/jpeg"
	case b[0] == 0x47 && b[1] == 0x49 && b[2] == 0x46:
		return "image/gif"
	case b[0] == 0x00 && b[1] == 0x00 && b[2] == 0x01 && b[3] == 0x00:
		return "image/x-icon"
	case len(b) >= 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	// SVG
	s := strings.TrimSpace(string(b[:min(len(b), 64)]))
	if strings.HasPrefix(s, "<?xml") || strings.HasPrefix(s, "<svg") {
		return "image/svg+xml"
	}
	return ""
}

// firstGroup returns the first non-empty submatch from a regex match (offset 1).
func firstGroup(m [][]byte) string {
	for _, g := range m[1:] {
		if len(g) > 0 {
			return string(g)
		}
	}
	return ""
}

type iconLink struct {
	href, sizes, rel string
}

// parseHTMLIcons fetches the root page of base and extracts <link rel="icon"> entries.
func parseHTMLIcons(base *url.URL) []iconLink {
	resp, err := favClient.Get(base.String())
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	// Read only the first 64 KB — enough to cover <head> on any real page.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil
	}

	var icons []iconLink
	for _, m := range reLinkTag.FindAllSubmatch(body, -1) {
		attrs := m[1]

		relM := reAttrRel.FindSubmatch(attrs)
		if relM == nil {
			continue
		}
		rel := strings.ToLower(firstGroup(relM))

		// Check if any space-separated token is an icon rel.
		isIcon := false
		for _, tok := range strings.Fields(rel) {
			if tok == "icon" || tok == "apple-touch-icon" || tok == "shortcut" {
				isIcon = true
				break
			}
		}
		if !isIcon {
			continue
		}

		hrefM := reAttrHref.FindSubmatch(attrs)
		if hrefM == nil {
			continue
		}
		href := strings.TrimSpace(firstGroup(hrefM))
		if href == "" || strings.HasPrefix(href, "data:") {
			continue
		}

		resolved, err := base.Parse(href)
		if err != nil {
			continue
		}

		sizes := ""
		if sM := reAttrSize.FindSubmatch(attrs); sM != nil {
			sizes = firstGroup(sM)
		}

		icons = append(icons, iconLink{href: resolved.String(), sizes: sizes, rel: rel})
	}
	return icons
}

// bestIconURL picks the highest-quality icon from a list.
// Priority: apple-touch-icon > SVG > largest declared size > first match.
func bestIconURL(icons []iconLink) string {
	best := ""
	bestScore := -1

	for _, ic := range icons {
		score := 0
		if strings.Contains(ic.rel, "apple-touch-icon") {
			score += 2000
		}
		// Prefer SVG (resolution-independent)
		if strings.HasSuffix(strings.ToLower(ic.href), ".svg") {
			score += 500
		}
		// Score by declared pixel size
		for _, sz := range strings.Fields(ic.sizes) {
			parts := strings.SplitN(strings.ToLower(sz), "x", 2)
			if len(parts) == 2 {
				if w, err := strconv.Atoi(parts[0]); err == nil {
					score += w
				}
			}
		}
		if score > bestScore {
			bestScore = score
			best = ic.href
		}
	}
	return best
}

// resolveFavicon tries, in order:
//  1. HTML <link rel="icon"> / <link rel="apple-touch-icon"> on the root page
//     — these usually reference high-resolution PNGs (96 px, 144 px, 180 px…)
//  2. /favicon.ico on the target host (classic fallback, often low-res)
//  3. Google's favicon service (non-local hosts only, last resort)
func resolveFavicon(rawURL string) ([]byte, string) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, ""
	}
	base := &url.URL{Scheme: parsed.Scheme, Host: parsed.Host}

	// 1. HTML link tags — bestIconURL scores by size, so we get the
	//    highest-resolution variant the site declares (e.g. YouTube 144 px).
	if icons := parseHTMLIcons(base); len(icons) > 0 {
		if best := bestIconURL(icons); best != "" {
			if data, ct := fetchImage(best); data != nil {
				return data, ct
			}
		}
	}

	// 2. Classic /favicon.ico (works even for auth-protected services
	//    whose root page returns a login redirect without icon links).
	if data, ct := fetchImage(base.String() + "/favicon.ico"); data != nil {
		return data, ct
	}

	// 3. Google favicon service — skipped for local/private addresses.
	if !isLocalHostname(parsed.Host) {
		googleURL := "https://www.google.com/s2/favicons?domain=" +
			url.QueryEscape(parsed.Hostname()) + "&sz=128"
		if data, ct := fetchImage(googleURL); data != nil {
			return data, ct
		}
	}

	return nil, ""
}

// faviconCacheHash returns a stable filename-safe key for a URL.
func faviconCacheHash(rawURL string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return hex.EncodeToString(sum[:])
}

const faviconSentinelTTL = 24 * time.Hour

// faviconFromDisk checks the on-disk cache.
// Returns (data, contentType, cacheHit).
// cacheHit=true with nil data means "previously not found — don't retry yet".
func faviconFromDisk(hash string) ([]byte, string, bool) {
	matches, _ := filepath.Glob(filepath.Join(favCacheDir, hash+".*"))
	for _, m := range matches {
		ext := strings.TrimPrefix(filepath.Ext(m), ".")
		if ext == "404" {
			// Sentinels expire after 24 h so public sites that later add a
			// favicon are eventually discovered without manual intervention.
			if info, err := os.Stat(m); err == nil && time.Since(info.ModTime()) < faviconSentinelTTL {
				return nil, "", true // still valid
			}
			os.Remove(m) //nolint:errcheck  expired — fall through to a fresh resolve
			return nil, "", false
		}
		if mime, ok := extToMIME[ext]; ok {
			data, err := os.ReadFile(m)
			if err == nil && len(data) > 0 {
				return data, mime, true
			}
		}
	}
	return nil, "", false
}

// faviconToDisk writes a resolved favicon (or a not-found sentinel) to disk.
// rawURL is required so we can decide whether to write a sentinel:
// local/private hosts are never sentinelled — they might simply not be
// running yet and will come online later; retrying them is instant.
func faviconToDisk(hash string, data []byte, ct string, rawURL string) {
	if err := os.MkdirAll(favCacheDir, 0755); err != nil {
		log.Printf("favicon-cache: cannot create directory: %v", err)
		return
	}

	if data == nil {
		// Skip sentinel for local hosts — failures are instant and the
		// service may come online at any time.
		if parsed, err := url.Parse(rawURL); err == nil && isLocalHostname(parsed.Host) {
			return
		}
		// Public host with no favicon — write a timed sentinel.
		fpath := filepath.Join(favCacheDir, hash+".404")
		os.WriteFile(fpath, nil, 0644) //nolint:errcheck
		return
	}

	ext, ok := mimeToExt[ct]
	if !ok {
		ext = "ico"
	}
	fpath := filepath.Join(favCacheDir, hash+"."+ext)
	if err := os.WriteFile(fpath, data, 0644); err != nil {
		log.Printf("favicon-cache: write %s: %v", fpath, err)
	}
}

// faviconProxyHandler resolves and serves a site favicon, backed by an
// on-disk cache that survives server restarts and crashes.
func faviconProxyHandler(w http.ResponseWriter, r *http.Request) {
	rawURL := r.URL.Query().Get("url")
	if rawURL == "" {
		http.NotFound(w, r)
		return
	}

	hash := faviconCacheHash(rawURL)

	// Disk cache hit
	if data, ct, hit := faviconFromDisk(hash); hit {
		if data == nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(data) //nolint:errcheck
		return
	}

	// Cache miss — resolve via HTTP
	data, ct := resolveFavicon(rawURL)
	go faviconToDisk(hash, data, ct, rawURL) // write in background; don't block the response

	if data == nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(data) //nolint:errcheck
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func slugify(s string) string {
	s = strings.ToLower(s)
	s = slugRe.ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

func hardcodedID(name string) string { return "hc-" + slugify(name) }

func generateID() string {
	b := make([]byte, 8)
	rand.Read(b) //nolint:errcheck
	return fmt.Sprintf("%x", b)
}

// ---------------------------------------------------------------------------
// Config loading
// ---------------------------------------------------------------------------

func loadConfig() {
	configPath := os.Getenv("HOMELAB_CONFIG")
	if configPath == "" {
		configPath = "config.json"
	}

	config = Config{
		Port:  8080,
		Title: "Dashboard",
		Services: map[string]ServiceConfig{
			"Portainer":      {Port: 9000, URL: "http://localhost:9000"},
			"Grafana":        {Port: 3000, URL: "http://localhost:3000"},
			"Prometheus":     {Port: 9090, URL: "http://localhost:9090"},
			"NextCloud":      {Port: 8081, URL: "http://localhost:8081"},
			"Home Assistant": {Port: 8123, URL: "http://localhost:8123"},
			"Pi-hole":        {Port: 8082, URL: "http://localhost:8082"},
		},
	}

	if file, err := os.Open(configPath); err == nil {
		defer file.Close()
		var c Config
		if err := json.NewDecoder(file).Decode(&c); err == nil {
			config = c
		} else {
			log.Printf("Warning: could not parse %s: %v", configPath, err)
		}
	}

	if port := os.Getenv("HOMELAB_PORT"); port != "" {
		if p, err := strconv.Atoi(port); err == nil {
			config.Port = p
		}
	}
	if title := os.Getenv("HOMELAB_TITLE"); title != "" {
		config.Title = title
	}

	log.Printf("Config loaded: port=%d services=%d shortcuts-file=%s",
		config.Port, len(config.Services), shortcutsPath)
}

// ---------------------------------------------------------------------------
// Shortcuts persistence
// ---------------------------------------------------------------------------

func loadShortcuts() {
	userDataMu.Lock()
	defer userDataMu.Unlock()

	userData = ShortcutsFile{
		Shortcuts: []Shortcut{},
		Order:     []string{},
	}

	raw, err := os.ReadFile(shortcutsPath)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("Warning: could not read %s: %v", shortcutsPath, err)
		}
		applySettingsDefaults(&userData.Settings)
		return
	}

	if err := json.Unmarshal(raw, &userData); err != nil {
		log.Printf("Warning: could not parse %s: %v", shortcutsPath, err)
	}

	if userData.Shortcuts == nil {
		userData.Shortcuts = []Shortcut{}
	}
	if userData.Order == nil {
		userData.Order = []string{}
	}
	applySettingsDefaults(&userData.Settings)
}

func applySettingsDefaults(s *Settings) {
	if s.BgColor1 == "" {
		s.BgColor1 = "#2c3928"
	}
	if s.BgColor2 == "" {
		s.BgColor2 = "#1b2619"
	}
	if s.BgAngle == 0 {
		s.BgAngle = 135
	}
}

// saveShortcuts must be called with userDataMu write-locked.
func saveShortcuts() error {
	raw, err := json.MarshalIndent(userData, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(shortcutsPath, raw, 0644)
}

// ---------------------------------------------------------------------------
// Merging hardcoded + user shortcuts
// ---------------------------------------------------------------------------

func getHardcodedShortcuts() []Shortcut {
	out := make([]Shortcut, 0, len(config.Services))
	for name, svc := range config.Services {
		out = append(out, Shortcut{
			ID:        hardcodedID(name),
			Name:      name,
			URL:       svc.URL,
			Hardcoded: true,
		})
	}
	return out
}

func getMergedShortcuts() []Shortcut {
	userDataMu.RLock()
	defer userDataMu.RUnlock()

	hardcoded := getHardcodedShortcuts()

	byID := make(map[string]Shortcut, len(hardcoded)+len(userData.Shortcuts))
	for _, s := range hardcoded {
		byID[s.ID] = s
	}
	for _, s := range userData.Shortcuts {
		byID[s.ID] = s
	}

	result := make([]Shortcut, 0, len(byID)+1)
	seen := make(map[string]bool, len(byID))

	for _, id := range userData.Order {
		if s, ok := byID[id]; ok {
			result = append(result, s)
			seen[id] = true
		}
	}

	// Hardcoded shortcuts not yet in order: append sorted by name.
	var unordered []Shortcut
	for _, s := range hardcoded {
		if !seen[s.ID] {
			unordered = append(unordered, s)
		}
	}
	sort.Slice(unordered, func(i, j int) bool {
		return unordered[i].Name < unordered[j].Name
	})
	result = append(result, unordered...)

	for _, s := range userData.Shortcuts {
		if !seen[s.ID] {
			result = append(result, s)
		}
	}

	return result
}

// ---------------------------------------------------------------------------
// HTTP handlers
// ---------------------------------------------------------------------------

func dashboardHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	shortcuts := getMergedShortcuts()
	shortcutsJSON, _ := json.Marshal(shortcuts)

	userDataMu.RLock()
	settings := userData.Settings
	userDataMu.RUnlock()
	settingsJSON, _ := json.Marshal(settings)

	type PageData struct {
		Title         string
		ShortcutsJSON template.JS
		SettingsJSON  template.JS
	}

	data := PageData{
		Title:         config.Title,
		ShortcutsJSON: template.JS(shortcutsJSON),
		SettingsJSON:  template.JS(settingsJSON),
	}

	templatePaths := []string{
		"templates/dashboard.html",
		"/usr/share/homelab-dashboard/templates/dashboard.html",
		filepath.Join(filepath.Dir(os.Args[0]), "../share/homelab-dashboard/templates/dashboard.html"),
	}

	var tmpl *template.Template
	for _, path := range templatePaths {
		if _, err := os.Stat(path); err == nil {
			tmpl, _ = template.ParseFiles(path)
			if tmpl != nil {
				break
			}
		}
	}

	if tmpl == nil {
		http.Error(w, "template not found", http.StatusInternalServerError)
		return
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes()) //nolint:errcheck
}

// shortcutsAPIHandler is registered for both /api/shortcuts and /api/shortcuts/
func shortcutsAPIHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	sub := strings.TrimPrefix(r.URL.Path, "/api/shortcuts")

	switch {
	case sub == "" || sub == "/":
		switch r.Method {
		case http.MethodGet:
			shortcuts := getMergedShortcuts()
			json.NewEncoder(w).Encode(shortcuts) //nolint:errcheck

		case http.MethodPost:
			var in struct {
				Name string `json:"name"`
				URL  string `json:"url"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil ||
				strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.URL) == "" {
				http.Error(w, `{"error":"name and url are required"}`, http.StatusBadRequest)
				return
			}

			sc := Shortcut{
				ID:   generateID(),
				Name: strings.TrimSpace(in.Name),
				URL:  strings.TrimSpace(in.URL),
			}

			userDataMu.Lock()
			userData.Shortcuts = append(userData.Shortcuts, sc)
			userData.Order = append(userData.Order, sc.ID)
			err := saveShortcuts()
			userDataMu.Unlock()

			if err != nil {
				http.Error(w, `{"error":"failed to save"}`, http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(sc) //nolint:errcheck

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}

	case sub == "/reorder":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var order []string
		if err := json.NewDecoder(r.Body).Decode(&order); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}
		userDataMu.Lock()
		userData.Order = order
		err := saveShortcuts()
		userDataMu.Unlock()
		if err != nil {
			http.Error(w, `{"error":"failed to save"}`, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)

	default:
		id := strings.TrimPrefix(sub, "/")
		if id == "" || strings.ContainsRune(id, '/') {
			http.NotFound(w, r)
			return
		}

		switch r.Method {
		case http.MethodPut:
			var in struct {
				Name string `json:"name"`
				URL  string `json:"url"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
				return
			}
			userDataMu.Lock()
			found := false
			for i, s := range userData.Shortcuts {
				if s.ID == id {
					if n := strings.TrimSpace(in.Name); n != "" {
						userData.Shortcuts[i].Name = n
					}
					if u := strings.TrimSpace(in.URL); u != "" {
						userData.Shortcuts[i].URL = u
					}
					found = true
					break
				}
			}
			var saveErr error
			if found {
				saveErr = saveShortcuts()
			}
			userDataMu.Unlock()

			if !found {
				http.Error(w, `{"error":"not found or not editable"}`, http.StatusNotFound)
				return
			}
			if saveErr != nil {
				http.Error(w, `{"error":"failed to save"}`, http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)

		case http.MethodDelete:
			userDataMu.Lock()
			found := false
			newShortcuts := make([]Shortcut, 0, len(userData.Shortcuts))
			for _, s := range userData.Shortcuts {
				if s.ID == id {
					found = true
				} else {
					newShortcuts = append(newShortcuts, s)
				}
			}
			newOrder := make([]string, 0, len(userData.Order))
			for _, oid := range userData.Order {
				if oid != id {
					newOrder = append(newOrder, oid)
				}
			}
			var saveErr error
			if found {
				userData.Shortcuts = newShortcuts
				userData.Order = newOrder
				saveErr = saveShortcuts()
			}
			userDataMu.Unlock()

			if !found {
				http.Error(w, `{"error":"not found or not deletable"}`, http.StatusNotFound)
				return
			}
			if saveErr != nil {
				http.Error(w, `{"error":"failed to save"}`, http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}
}

func settingsAPIHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		userDataMu.RLock()
		s := userData.Settings
		userDataMu.RUnlock()
		json.NewEncoder(w).Encode(s) //nolint:errcheck

	case http.MethodPut:
		var s Settings
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			http.Error(w, `{"error":"invalid body"}`, http.StatusBadRequest)
			return
		}
		applySettingsDefaults(&s)
		userDataMu.Lock()
		userData.Settings = s
		err := saveShortcuts()
		userDataMu.Unlock()
		if err != nil {
			http.Error(w, `{"error":"failed to save"}`, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "healthy"}) //nolint:errcheck
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

func main() {
	loadConfig()
	loadShortcuts()

	http.HandleFunc("/", dashboardHandler)
	http.HandleFunc("/api/shortcuts", shortcutsAPIHandler)
	http.HandleFunc("/api/shortcuts/", shortcutsAPIHandler)
	http.HandleFunc("/api/settings", settingsAPIHandler)
	http.HandleFunc("/api/favicon", faviconProxyHandler)
	http.HandleFunc("/health", healthHandler)

	addr := fmt.Sprintf(":%d", config.Port)
	log.Printf("Starting server on %s", addr)

	server := &http.Server{
		Addr:         addr,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
