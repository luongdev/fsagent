package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/luongdev/fsagent/pkg/connection"
	"github.com/luongdev/fsagent/pkg/logger"
	"github.com/luongdev/fsagent/pkg/metrics"
)

// HTTPServer represents the HTTP server for health and metrics endpoints
type HTTPServer struct {
	server       *http.Server
	connManager  connection.ConnectionManager
	port         int
	mu           sync.RWMutex
	shutdownChan chan struct{}
	stopped      bool
}

// NewHTTPServer creates a new HTTP server instance
func NewHTTPServer(port int, connManager connection.ConnectionManager) *HTTPServer {
	return &HTTPServer{
		port:         port,
		connManager:  connManager,
		shutdownChan: make(chan struct{}),
	}
}

// Start starts the HTTP server
func (s *HTTPServer) Start(ctx context.Context) error {
	mux := http.NewServeMux()

	// Register endpoints
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/ready", s.handleReady)
	mux.HandleFunc("/metrics", s.handleMetrics)

	s.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", s.port),
		Handler: mux,
	}

	logger.Info("HTTP server starting on port %d", s.port)

	// Start server in goroutine
	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("HTTP server error: %v", err)
		}
	}()

	// Wait for shutdown signal
	go func() {
		<-ctx.Done()
		s.Stop()
	}()

	return nil
}

// Stop gracefully stops the HTTP server
func (s *HTTPServer) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if already stopped
	if s.stopped {
		return nil
	}

	if s.server == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	logger.Info("Shutting down HTTP server...")
	if err := s.server.Shutdown(ctx); err != nil {
		logger.Error("HTTP server shutdown error: %v", err)
		return err
	}

	close(s.shutdownChan)
	s.stopped = true
	logger.Info("HTTP server stopped")
	return nil
}

// HealthResponse represents the response structure for /health endpoint
type HealthResponse struct {
	Status      string                    `json:"status"`
	Connections map[string]ConnectionInfo `json:"connections"`
}

// ConnectionInfo represents connection information for a FreeSWITCH instance
type ConnectionInfo struct {
	Connected bool   `json:"connected"`
	LastEvent string `json:"last_event,omitempty"`
	LastError string `json:"last_error,omitempty"`
}

// handleHealth handles the /health endpoint
func (s *HTTPServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get connection status from connection manager
	connStatus := s.connManager.GetStatus()

	// Build response
	response := HealthResponse{
		Status:      "healthy",
		Connections: make(map[string]ConnectionInfo),
	}

	hasActiveConnection := false

	for instanceName, status := range connStatus {
		info := ConnectionInfo{
			Connected: status.Connected,
		}

		// Format last event timestamp
		if !status.LastEventAt.IsZero() {
			info.LastEvent = status.LastEventAt.Format(time.RFC3339)
		}

		// Include error message if present
		if status.LastError != nil {
			info.LastError = status.LastError.Error()
		}

		response.Connections[instanceName] = info

		if status.Connected {
			hasActiveConnection = true
		}
	}

	// Set overall status based on connections
	if !hasActiveConnection {
		response.Status = "unhealthy"
	}

	// Set response headers
	w.Header().Set("Content-Type", "application/json")

	// Set status code based on health
	statusCode := http.StatusOK
	if response.Status == "unhealthy" {
		statusCode = http.StatusServiceUnavailable
	}
	w.WriteHeader(statusCode)

	// Encode and send response
	if err := json.NewEncoder(w).Encode(response); err != nil {
		logger.Warn("Failed to encode health response: %v", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	logger.Debug("Health check request processed: status=%s", response.Status)
}

// handleReady handles the /ready endpoint
func (s *HTTPServer) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get connection status from connection manager
	connStatus := s.connManager.GetStatus()

	// Check if at least one connection is active
	hasActiveConnection := false
	for _, status := range connStatus {
		if status.Connected {
			hasActiveConnection = true
			break
		}
	}

	// Return 200 if at least one connection is active, 503 otherwise
	if hasActiveConnection {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ready"))
		logger.Debug("Readiness check: ready (at least one connection active)")
	} else {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("not ready"))
		logger.Debug("Readiness check: not ready (no active connections)")
	}
}

// handleMetrics handles the /metrics endpoint (Prometheus format)
func (s *HTTPServer) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get metrics from global metrics instance
	m := metrics.GetMetrics()
	metricsOutput := m.GetPrometheusMetrics()

	// Set response headers
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.WriteHeader(http.StatusOK)

	// Write metrics
	w.Write([]byte(metricsOutput))

	logger.Debug("Metrics endpoint accessed")
}
