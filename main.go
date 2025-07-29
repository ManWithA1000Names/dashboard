package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"
)

type ServiceConfig struct {
	Port int    `json:"port"`
	URL  string `json:"url"`
}

type Config struct {
	Port     int                      `json:"port"`
	Services map[string]ServiceConfig `json:"services"`
	Title    string                   `json:"title"`
}

type ServiceStatus struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Status string `json:"status"`
	Online bool   `json:"online"`
}

type DashboardData struct {
	Title             string
	Services          []ServiceStatus
	OnlineCount       int
	OfflineCount      int
	OperationalStatus string
	LastUpdated       string
}

var (
	config        Config
	lastCacheTime time.Time
)

func loadConfig() {
	configPath := os.Getenv("HOMELAB_CONFIG")
	if configPath == "" {
		configPath = "config.json"
	}

	// Default configuration with new format
	config = Config{
		Port:  8080,
		Title: "Local Cloud Control Center",
		Services: map[string]ServiceConfig{
			"Portainer":      {Port: 9000, URL: "http://localhost:9000"},
			"Grafana":        {Port: 3000, URL: "http://localhost:3000"},
			"Prometheus":     {Port: 9090, URL: "http://localhost:9090"},
			"NextCloud":      {Port: 8081, URL: "http://localhost:8081"},
			"Home Assistant": {Port: 8123, URL: "http://localhost:8123"},
			"Pi-hole":        {Port: 8082, URL: "http://localhost:8082"},
		},
	}

	// Load from environment variables if available
	if port := os.Getenv("HOMELAB_PORT"); port != "" {
		if p, err := strconv.Atoi(port); err == nil {
			config.Port = p
		}
	}

	if title := os.Getenv("HOMELAB_TITLE"); title != "" {
		config.Title = title
	}

	// Load services from environment variable (JSON format) - support old format
	if servicesJSON := os.Getenv("HOMELAB_SERVICES"); servicesJSON != "" {
		var services map[string]ServiceConfig
		if err := json.Unmarshal([]byte(servicesJSON), &services); err == nil {
			config.Services = services
		}
	}

	// Try to load from config file
	if file, err := os.Open(configPath); err == nil {
		defer file.Close()

		// First try to decode as new format
		var newConfig Config
		decoder := json.NewDecoder(file)
		if err := decoder.Decode(&newConfig); err == nil {
			config = newConfig
		} else {
			log.Printf("Error decoding config file: %v", err)
		}
	}

	log.Printf("Loaded configuration: Port=%d, Services=%d", config.Port, len(config.Services))
}

func checkServicePort(name string, port int, publicURL string, timeout time.Duration) ServiceStatus {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("localhost:%d", port), timeout)
	if err != nil {
		return ServiceStatus{
			Name:   name,
			URL:    publicURL,
			Status: "Offline",
			Online: false,
		}
	}
	defer conn.Close()

	return ServiceStatus{
		Name:   name,
		URL:    publicURL,
		Status: "Online",
		Online: true,
	}
}

func checkAllServices() []ServiceStatus {
	var wg sync.WaitGroup
	results := make(chan ServiceStatus, len(config.Services))

	timeout := 3 * time.Second

	for name, serviceConfig := range config.Services {
		wg.Add(1)
		go func(name string, serviceConfig ServiceConfig) {
			defer wg.Done()

			// Check service availability by port only
			status := checkServicePort(name, serviceConfig.Port, serviceConfig.URL, timeout)

			results <- status
		}(name, serviceConfig)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var services []ServiceStatus
	for status := range results {
		services = append(services, status)
	}

	// Sort services by name
	sort.Slice(services, func(i, j int) bool {
		return services[i].Name < services[j].Name
	})

	return services
}

func getOperationalStatus(services []ServiceStatus) string {
	online := 0

	for _, service := range services {
		if service.Online {
			online++
		}
	}

	switch online {
	case len(services):
		return "Operational"
	case 0:
		return "Critical"
	default:
		return "Limited"
	}
}

func dashboardHandler(w http.ResponseWriter, r *http.Request) {
	services := checkAllServices()

	online := 0
	for _, service := range services {
		if service.Online {
			online++
		}
	}

	data := DashboardData{
		Title:             config.Title,
		Services:          services,
		OnlineCount:       online,
		OfflineCount:      len(services) - online,
		OperationalStatus: getOperationalStatus(services),
		LastUpdated:       time.Now().Format("15:04:05"),
	}

	// Look for template in multiple locations
	templatePaths := []string{
		"templates/dashboard.html",                                                                     // Local development
		"/usr/share/homelab-dashboard/templates/dashboard.html",                                        // System install
		filepath.Join(filepath.Dir(os.Args[0]), "../share/homelab-dashboard/templates/dashboard.html"), // Nix store
	}

	var tmpl *template.Template
	var err error

	for _, path := range templatePaths {
		if _, statErr := os.Stat(path); statErr == nil {
			tmpl, err = template.ParseFiles(path)
			if err == nil {
				break
			}
		}
	}

	if tmpl == nil {
		http.Error(w, "Template not found in any location", http.StatusInternalServerError)
		return
	}

	// Execute template into a buffer first to prevent partial writes
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		http.Error(w, "Template execution error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Set content type and write the complete response
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(buf.Bytes())
}

func apiHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	services := checkAllServices()

	response := map[string]any{
		"services":    services,
		"lastUpdated": time.Now().Format(time.RFC3339),
		"operational": getOperationalStatus(services),
	}

	json.NewEncoder(w).Encode(response)
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "healthy"})
}

func main() {
	loadConfig()

	http.HandleFunc("/", dashboardHandler)
	http.HandleFunc("/api/services", apiHandler)
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
