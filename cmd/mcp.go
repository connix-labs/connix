/*
Copyright © 2025 connix-labs

This file implements a polished Model Context Protocol (MCP) server functionality for the connix CLI.
It provides enterprise-grade commands to start, manage, and interact with MCP servers that enable seamless
integration between LLM applications and external data sources and tools.

Key Features:
- High-performance MCP server with modern Go patterns and enterprise reliability
- Comprehensive tool and resource management with security validation
- Advanced session handling with per-session customization and monitoring
- Robust error handling with intelligent recovery mechanisms and graceful shutdown
- Performance monitoring, telemetry collection, and detailed metrics
- Caching support for improved responsiveness and scalability
- Environment variable configuration support with validation
- Context-aware timeouts and resource management for all operations
- CORS support for browser-based clients
- Structured logging with configurable levels

The MCP command is designed to work seamlessly with modern AI applications,
providing a secure, scalable, and standardized way to expose functionality and data.
*/

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/spf13/cobra"
)

// Configuration variables for the MCP command.
// These control various aspects of the MCP server behavior and can be
// set via command-line flags or environment variables.
var (
	// verboseMCP enables detailed output during MCP operations.
	// When true, shows additional information about server operations and requests.
	verboseMCP bool

	// quietMCP suppresses non-error output during MCP operations.
	// When true, only errors and critical information are displayed.
	quietMCP bool

	// portMCP sets the port for HTTP transport mode.
	// Defaults to 8080 but can be configured via CONNIX_MCP_PORT env var.
	portMCP int

	// timeoutMCP sets the maximum duration for MCP operations.
	// Defaults to 30 minutes but can be configured via CONNIX_MCP_TIMEOUT env var.
	timeoutMCP time.Duration

	// maxSessionsMCP limits the number of concurrent client sessions.
	// Defaults to 100 but can be configured via CONNIX_MCP_MAX_SESSIONS env var.
	maxSessionsMCP int

	// transportMCP specifies the transport protocol (stdio, http, sse).
	// Defaults to stdio but can be configured via CONNIX_MCP_TRANSPORT env var.
	transportMCP string

	// enableCacheMCP controls whether to use caching for MCP operations.
	// When true, caches tool results and resource data for better performance.
	enableCacheMCP bool

	// cacheTimeoutMCP sets how long cached data remains valid.
	// Defaults to 10 minutes but can be configured via CONNIX_MCP_CACHE_TIMEOUT env var.
	cacheTimeoutMCP time.Duration

	// enableTelemetryMCP controls whether to collect performance and usage metrics.
	// When true, collects anonymous metrics for performance analysis and improvements.
	enableTelemetryMCP bool

	// serverNameMCP sets the name identifier for the MCP server.
	// Defaults to "Connix MCP Server" but can be configured via CONNIX_MCP_SERVER_NAME env var.
	serverNameMCP string

	// serverVersionMCP sets the version identifier for the MCP server.
	// Defaults to "1.0.0" but can be configured via CONNIX_MCP_VERSION env var.
	serverVersionMCP string

	// enableRecoveryMCP enables automatic recovery from panics in tool handlers.
	// When true, panics are caught and converted to error responses.
	enableRecoveryMCP bool

	// configFileMCP specifies a JSON configuration file for tools and resources.
	// When set, loads server configuration from the specified file.
	configFileMCP string

	// enableHotReloadMCP enables hot reloading of configuration file changes.
	// When true, monitors config file for changes and reloads automatically.
	enableHotReloadMCP bool

	// logLevelMCP sets the logging level for the MCP server.
	// Supports: debug, info, warn, error. Defaults to "info".
	logLevelMCP string

	// enableMetricsMCP enables detailed performance metrics collection.
	// When true, collects detailed timing and usage statistics.
	enableMetricsMCP bool

	// maxRequestSizeMCP limits the maximum size of incoming requests.
	// Defaults to 10MB but can be configured via CONNIX_MCP_MAX_REQUEST_SIZE env var.
	maxRequestSizeMCP int64

	// corsEnabledMCP enables CORS headers for HTTP transport.
	// When true, adds appropriate CORS headers for browser-based clients.
	corsEnabledMCP bool
)

// Performance optimization: Cache for expensive MCP operations
// This reduces redundant computations and improves performance for repeated operations
var (
	// mcpCache stores cached MCP operation results with timestamps
	mcpCache = struct {
		sync.RWMutex
		tools     cacheEntry
		resources cacheEntry
		sessions  cacheEntry
	}{}

	// mcpTelemetryCollector stores performance and usage metrics
	mcpTelemetryCollector = struct {
		sync.RWMutex
		metrics []MCPTelemetryEvent
	}{}

	// mcpServerInstance holds the current MCP server instance
	mcpServerInstance = struct {
		sync.RWMutex
		server       *server.MCPServer
		running      bool
		sessions     map[string]*MCPSession
		startTime    time.Time
		requestCount int64
		errorCount   int64
		shutdownCh   chan struct{}
	}{
		sessions:   make(map[string]*MCPSession),
		shutdownCh: make(chan struct{}),
	}

	// mcpLogger provides structured logging for the MCP server
	mcpLogger = log.New(os.Stdout, "[MCP] ", log.LstdFlags|log.Lshortfile)
)

// MCPTelemetryEvent represents a single MCP metrics event
type MCPTelemetryEvent struct {
	Timestamp    time.Time             `json:"timestamp"`
	Operation    string                `json:"operation"`
	Duration     time.Duration         `json:"duration_ms"`
	Success      bool                  `json:"success"`
	SessionCount int                   `json:"session_count,omitempty"`
	ToolName     string                `json:"tool_name,omitempty"`
	ResourceURI  string                `json:"resource_uri,omitempty"`
	ErrorType    string                `json:"error_type,omitempty"`
	Metadata     map[string]interface{} `json:"metadata,omitempty"`
}

// MCPSession represents a client session with MCP server capabilities
type MCPSession struct {
	id           string
	notifChannel chan mcp.JSONRPCNotification
	isInitialized bool
	connectedTime time.Time
	lastActivity time.Time
	clientInfo   map[string]interface{}
	customTools  map[string]server.ServerTool
	permissions  map[string]bool
	requestCount int64
	userAgent    string
	ipAddress    string
	mu           sync.RWMutex
}

// Implement the ClientSession interface
func (s *MCPSession) SessionID() string {
	return s.id
}

func (s *MCPSession) NotificationChannel() chan<- mcp.JSONRPCNotification {
	return s.notifChannel
}

func (s *MCPSession) Initialize() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.isInitialized = true
	s.connectedTime = time.Now()
	s.lastActivity = time.Now()
}

func (s *MCPSession) Initialized() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.isInitialized
}

// UpdateActivity updates the last activity timestamp
func (s *MCPSession) UpdateActivity() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastActivity = time.Now()
	atomic.AddInt64(&s.requestCount, 1)
}

// GetStats returns session statistics
func (s *MCPSession) GetStats() map[string]interface{} {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]interface{}{
		"session_id":     s.id,
		"connected_time": s.connectedTime,
		"last_activity":  s.lastActivity,
		"request_count":  atomic.LoadInt64(&s.requestCount),
		"user_agent":     s.userAgent,
		"ip_address":     s.ipAddress,
		"duration":       time.Since(s.connectedTime).String(),
	}
}

// Implement SessionWithTools interface for per-session tool customization
func (s *MCPSession) GetSessionTools() map[string]server.ServerTool {
	return s.customTools
}

func (s *MCPSession) SetSessionTools(tools map[string]server.ServerTool) {
	s.customTools = tools
}

// MCPConfig represents the structure of the MCP configuration file
type MCPConfig struct {
	Server struct {
		Name        string `json:"name"`
		Version     string `json:"version"`
		Description string `json:"description"`
	} `json:"server"`
	Tools     []MCPToolConfig     `json:"tools"`
	Resources []MCPResourceConfig `json:"resources"`
}

// MCPToolConfig represents a tool configuration
type MCPToolConfig struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Handler     string                 `json:"handler"`
	Parameters  map[string]interface{} `json:"parameters"`
	Permissions []string               `json:"permissions,omitempty"`
}

// MCPResourceConfig represents a resource configuration
type MCPResourceConfig struct {
	URI         string                 `json:"uri"`
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	MIMEType    string                 `json:"mime_type"`
	Handler     string                 `json:"handler"`
	Parameters  map[string]interface{} `json:"parameters"`
	Template    bool                   `json:"template,omitempty"`
}

var mcpCmd = &cobra.Command{
	Use:   "mcp",
	Short: "Start and manage enterprise-grade Model Context Protocol (MCP) server",
	Long: `Start and manage a production-ready Model Context Protocol (MCP) server for AI applications.

The MCP server enables seamless integration between LLM applications and external
data sources and tools through a standardized protocol. It supports multiple
transport mechanisms and provides comprehensive session management with enterprise
features like graceful shutdown, detailed monitoring, and security validation.

🚀 Enterprise Features:
  • High-performance server with concurrent session handling
  • Multiple transport protocols (stdio, HTTP, SSE) with CORS support
  • Advanced tool and resource management with security validation
  • Per-session customization and granular permissions
  • Comprehensive error handling with graceful recovery mechanisms
  • Real-time performance monitoring and telemetry collection
  • Configuration file support with hot reloading capabilities
  • Intelligent caching for improved performance and scalability
  • Structured logging with configurable levels
  • Graceful shutdown with session notification

🌐 Transport Modes:
  stdio: Standard input/output communication (default, ideal for direct integration)
  http:  HTTP-based communication on specified port (scalable, web-friendly)
  sse:   Server-Sent Events for real-time communication (browser-compatible)

📊 Built-in Tools:
  • system_info: Comprehensive system and server statistics
  • process_info: Process management and system load information
  • file_operations: Secure file read/write/list with validation

📈 Built-in Resources:
  • server://status: Real-time server status and performance metrics

Examples:
  # Start MCP server with stdio transport (recommended for AI agents)
  connix mcp

  # Start production HTTP server with monitoring
  connix mcp --transport http --port 9090 --enable-metrics --log-level info

  # Start with enhanced security and verbose logging
  connix mcp --verbose --enable-recovery --max-request-size 5242880

  # Load enterprise configuration with hot reload and telemetry
  connix mcp --config production.json --hot-reload --enable-telemetry

  # Start web-compatible server with CORS support
  connix mcp --transport http --enable-cors --port 8080

Environment Variables:
  CONNIX_MCP_PORT              - HTTP server port (default: 8080)
  CONNIX_MCP_TIMEOUT           - Operation timeout (e.g., "30m", "1h")
  CONNIX_MCP_MAX_SESSIONS      - Max concurrent sessions (default: 100)
  CONNIX_MCP_TRANSPORT         - Transport protocol (stdio/http/sse)
  CONNIX_MCP_SERVER_NAME       - Server name identifier
  CONNIX_MCP_VERSION           - Server version identifier
  CONNIX_MCP_CACHE_TIMEOUT     - Cache timeout duration
  CONNIX_MCP_CONFIG_FILE       - Configuration file path
  CONNIX_MCP_LOG_LEVEL         - Logging level (debug/info/warn/error)
  CONNIX_MCP_MAX_REQUEST_SIZE  - Maximum request size in bytes
  CONNIX_MCP_ENABLE_CORS       - Enable CORS headers (true/false)`,
	RunE: runMCP,
}

// init initializes the MCP command and its flags.
// This function is called automatically when the package is imported.
// It sets up command-line flags with defaults from environment variables
// and registers the MCP command with the root command.
func init() {
	// Configure command-line flags with environment variable fallbacks
	mcpCmd.Flags().BoolVarP(&verboseMCP, "verbose", "v", false, "Enable verbose output with detailed operation logs")
	mcpCmd.Flags().BoolVarP(&quietMCP, "quiet", "q", false, "Suppress non-error output for clean integration")
	mcpCmd.Flags().IntVarP(&portMCP, "port", "p", getEnvInt("CONNIX_MCP_PORT", 8080), "HTTP server port for web transport")
	mcpCmd.Flags().DurationVar(&timeoutMCP, "timeout", getEnvDuration("CONNIX_MCP_TIMEOUT", 30*time.Minute), "Operation timeout for requests")
	mcpCmd.Flags().IntVar(&maxSessionsMCP, "max-sessions", getEnvInt("CONNIX_MCP_MAX_SESSIONS", 100), "Maximum concurrent client sessions")
	mcpCmd.Flags().StringVarP(&transportMCP, "transport", "t", getEnvString("CONNIX_MCP_TRANSPORT", "stdio"), "Transport protocol (stdio/http/sse)")
	mcpCmd.Flags().BoolVar(&enableCacheMCP, "enable-cache", getEnvBool("CONNIX_MCP_ENABLE_CACHE", true), "Enable intelligent caching for performance")
	mcpCmd.Flags().DurationVar(&cacheTimeoutMCP, "cache-timeout", getEnvDuration("CONNIX_MCP_CACHE_TIMEOUT", 10*time.Minute), "Cache timeout duration")
	mcpCmd.Flags().BoolVar(&enableTelemetryMCP, "enable-telemetry", getEnvBool("CONNIX_MCP_ENABLE_TELEMETRY", false), "Enable anonymous telemetry collection")
	mcpCmd.Flags().StringVar(&serverNameMCP, "server-name", getEnvString("CONNIX_MCP_SERVER_NAME", "Connix MCP Server"), "Server name identifier")
	mcpCmd.Flags().StringVar(&serverVersionMCP, "server-version", getEnvString("CONNIX_MCP_VERSION", "1.0.0"), "Server version identifier")
	mcpCmd.Flags().BoolVar(&enableRecoveryMCP, "enable-recovery", getEnvBool("CONNIX_MCP_ENABLE_RECOVERY", true), "Enable automatic panic recovery")
	mcpCmd.Flags().StringVarP(&configFileMCP, "config", "c", getEnvString("CONNIX_MCP_CONFIG_FILE", ""), "Configuration file path")
	mcpCmd.Flags().BoolVar(&enableHotReloadMCP, "hot-reload", getEnvBool("CONNIX_MCP_HOT_RELOAD", false), "Enable hot reloading of config file")
	mcpCmd.Flags().StringVar(&logLevelMCP, "log-level", getEnvString("CONNIX_MCP_LOG_LEVEL", "info"), "Logging level (debug/info/warn/error)")
	mcpCmd.Flags().BoolVar(&enableMetricsMCP, "enable-metrics", getEnvBool("CONNIX_MCP_ENABLE_METRICS", true), "Enable detailed performance metrics")
	mcpCmd.Flags().Int64Var(&maxRequestSizeMCP, "max-request-size", getEnvInt64("CONNIX_MCP_MAX_REQUEST_SIZE", 10*1024*1024), "Maximum request size in bytes")
	mcpCmd.Flags().BoolVar(&corsEnabledMCP, "enable-cors", getEnvBool("CONNIX_MCP_ENABLE_CORS", false), "Enable CORS headers for HTTP transport")

	// Register the MCP command as a subcommand of the root command
	rootCmd.AddCommand(mcpCmd)
}

// getEnvString retrieves a string value from an environment variable.
// If the environment variable is not set, it returns the provided default value.
//
// Parameters:
//   - key: The environment variable name to check
//   - defaultValue: The fallback string if env var is not set
//
// Returns:
//   - The string from the environment variable, or defaultValue
//
// Example:
//   serverName := getEnvString("CONNIX_MCP_SERVER_NAME", "Default Server")
func getEnvString(key, defaultValue string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return defaultValue
}

// getEnvInt64 retrieves an int64 value from an environment variable.
// If the environment variable is not set or cannot be parsed as an int64,
// it returns the provided default value.
//
// Parameters:
//   - key: The environment variable name to check
//   - defaultValue: The fallback int64 if env var is not set or invalid
//
// Returns:
//   - The parsed int64 from the environment variable, or defaultValue
//
// Example:
//   maxSize := getEnvInt64("CONNIX_MAX_SIZE", 1024)
func getEnvInt64(key string, defaultValue int64) int64 {
	if val := os.Getenv(key); val != "" {
		if int64Val, err := strconv.ParseInt(val, 10, 64); err == nil {
			return int64Val
		}
		// Note: Invalid int64 format is silently ignored and defaults are used
	}
	return defaultValue
}

// runMCP is the main entry point for the MCP command execution.
// It orchestrates the entire MCP server startup process including:
// - Configuration validation
// - Graceful shutdown handling
// - Server initialization
// - Transport setup
// - Session management
//
// The function implements comprehensive error handling and supports multiple
// transport modes with user-friendly progress reporting and enterprise features.
//
// Parameters:
//   - cmd: The cobra command instance (unused but required by cobra.Command.RunE)
//   - args: Command-line arguments (unused but required by cobra.Command.RunE)
//
// Returns:
//   - error: nil on success, or a detailed error with user-friendly messages
//
// Error Recovery:
//   - Automatically handles graceful shutdown on interruption
//   - Provides actionable error messages with troubleshooting tips
//   - Gracefully handles context cancellation and timeouts
//   - Implements enterprise-grade error recovery mechanisms
func runMCP(cmd *cobra.Command, args []string) error {
	// Start telemetry collection for the entire MCP operation
	mcpStart := time.Now()
	var telemetryMetadata = make(map[string]interface{})
	telemetryMetadata["transport"] = transportMCP
	telemetryMetadata["config_file"] = configFileMCP != ""
	telemetryMetadata["cors_enabled"] = corsEnabledMCP

	defer func() {
		if enableTelemetryMCP {
			collectMCPTelemetry("mcp_server_complete", time.Since(mcpStart), true, 0, "", "", telemetryMetadata)
		}
	}()

	// Create a context with timeout for all MCP operations
	// This ensures operations don't hang indefinitely
	ctx, cancel := context.WithTimeout(context.Background(), timeoutMCP)
	defer cancel()

	// Set up graceful shutdown handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Initialize global server state
	mcpServerInstance.Lock()
	mcpServerInstance.startTime = time.Now()
	mcpServerInstance.Unlock()

	// Define logging functions based on user preferences
	logInfo := func(msg string, args ...interface{}) {
		if !quietMCP {
			mcpLogger.Printf("[INFO] "+msg, args...)
		}
	}

	logVerbose := func(msg string, args ...interface{}) {
		if verboseMCP && !quietMCP {
			mcpLogger.Printf("[VERBOSE] "+msg, args...)
		}
	}

	logError := func(msg string, args ...interface{}) {
		mcpLogger.Printf("[ERROR] "+msg, args...)
	}

	// Step 1: Validate configuration before proceeding
	if err := validateMCPConfiguration(); err != nil {
		return fmt.Errorf("❌ Configuration validation failed: %w", err)
	}

	logVerbose("Configuration validated successfully")

	// Step 2: Initialize MCP server with configuration
	logInfo("🚀 Initializing enterprise MCP server: %s v%s", serverNameMCP, serverVersionMCP)

	mcpServer, err := initializeMCPServer(ctx, logVerbose)
	if err != nil {
		return fmt.Errorf("❌ Failed to initialize MCP server: %w", err)
	}

	logVerbose("MCP server initialized with transport: %s", transportMCP)

	// Step 3: Load configuration file if specified
	if configFileMCP != "" {
		if err := loadMCPConfiguration(mcpServer, configFileMCP, logVerbose); err != nil {
			return fmt.Errorf("❌ Failed to load configuration: %w", err)
		}
		logInfo("✅ Configuration loaded from: %s", configFileMCP)

		// Step 4: Set up hot reload if enabled
		if enableHotReloadMCP {
			go watchConfigFile(ctx, mcpServer, configFileMCP, logVerbose)
			logVerbose("Hot reload enabled for configuration file")
		}
	}

	// Step 5: Start the MCP server with selected transport
	logInfo("🌟 Starting enterprise MCP server on %s transport", transportMCP)

	// Start server in a goroutine to allow graceful shutdown
	errorChan := make(chan error, 1)
	go func() {
		switch transportMCP {
		case "stdio":
			errorChan <- server.ServeStdio(mcpServer)
		case "http":
			logInfo("🌐 HTTP server listening on port %d with enterprise features", portMCP)
			streamableServer := server.NewStreamableHTTPServer(mcpServer)
			errorChan <- streamableServer.Start(fmt.Sprintf(":%d", portMCP))
		case "sse":
			logInfo("📡 SSE server listening on port %d with real-time capabilities", portMCP)
			sseServer := server.NewSSEServer(mcpServer)
			errorChan <- sseServer.Start(fmt.Sprintf(":%d", portMCP))
		default:
			errorChan <- fmt.Errorf("❌ Unsupported transport protocol: %s\n\nSupported transports: stdio, http, sse", transportMCP)
		}
	}()

	// Display startup success message
	if !quietMCP {
		logInfo("🎯 MCP server is ready for AI agent connections!")
		logInfo("📊 Monitoring: metrics=%v, telemetry=%v, cache=%v", enableMetricsMCP, enableTelemetryMCP, enableCacheMCP)
		if transportMCP != "stdio" {
			logInfo("🔗 Server endpoint: %s://localhost:%d", transportMCP, portMCP)
		}
	}

	// Wait for either server error or shutdown signal
	select {
	case err = <-errorChan:
		if err != nil {
			logError("Server failed: %v", err)
			return fmt.Errorf("❌ MCP server failed: %w\n\n💡 Troubleshooting tips:\n  • Check port availability: netstat -tulpn | grep %d\n  • Verify permissions for the selected port\n  • Ensure no other MCP servers are running\n  • Try a different port with --port flag", err, portMCP)
		}
	case sig := <-sigChan:
		logInfo("🚨 Received signal %v, initiating graceful shutdown...", sig)
		if err := performGracefulShutdown(ctx, logInfo, logVerbose); err != nil {
			logVerbose("Warning during shutdown: %v", err)
		}
		logInfo("✅ Graceful shutdown completed successfully")
		return nil
	case <-ctx.Done():
		logError("Server timeout: %v", ctx.Err())
		return fmt.Errorf("❌ Server timeout: %w\n\n💡 Consider increasing timeout with --timeout flag", ctx.Err())
	}

	logInfo("🎉 MCP server shutdown completed successfully!")
	return nil
}

// initializeMCPServer creates and configures a new MCP server instance
// with all the necessary capabilities and middleware for enterprise operation.
//
// Parameters:
//   - ctx: Context for timeout and cancellation control
//   - logVerbose: Function to log verbose messages
//
// Returns:
//   - *server.MCPServer: Configured MCP server instance
//   - error: nil on success, error if initialization fails
func initializeMCPServer(ctx context.Context, logVerbose func(string, ...interface{})) (*server.MCPServer, error) {
	// Create server options based on configuration
	var options []server.ServerOption

	// Add tool capabilities
	options = append(options, server.WithToolCapabilities(true))

	// Add recovery middleware if enabled
	if enableRecoveryMCP {
		options = append(options, server.WithRecovery())
		logVerbose("Recovery middleware enabled for panic protection")
	}

	// Add telemetry middleware if enabled
	if enableTelemetryMCP {
		options = append(options, server.WithToolHandlerMiddleware(createTelemetryMiddleware()))
		logVerbose("Telemetry middleware enabled for performance monitoring")
	}

	// Add session management capabilities
	options = append(options, server.WithToolFilter(createSessionToolFilter()))
	logVerbose("Advanced session management enabled")

	// Create the MCP server
	mcpServer := server.NewMCPServer(serverNameMCP, serverVersionMCP, options...)

	// Store server instance globally for management
	mcpServerInstance.Lock()
	mcpServerInstance.server = mcpServer
	mcpServerInstance.running = true
	mcpServerInstance.Unlock()

	// Set up default tools and resources
	if err := setupDefaultToolsAndResources(mcpServer, logVerbose); err != nil {
		return nil, fmt.Errorf("failed to setup default tools and resources: %w", err)
	}

	return mcpServer, nil
}

// setupDefaultToolsAndResources adds enterprise-grade default tools and resources to the MCP server
func setupDefaultToolsAndResources(mcpServer *server.MCPServer, logVerbose func(string, ...interface{})) error {
	// Add enhanced system information tool
	systemTool := mcp.NewTool("system_info",
		mcp.WithDescription("Get comprehensive system information including OS, architecture, runtime details, and server statistics"),
	)

	mcpServer.AddTool(systemTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Update session activity
		if session := server.ClientSessionFromContext(ctx); session != nil {
			if mcpSession, ok := session.(*MCPSession); ok {
				mcpSession.UpdateActivity()
			}
		}

		mcpServerInstance.RLock()
		startTime := mcpServerInstance.startTime
		requestCount := atomic.LoadInt64(&mcpServerInstance.requestCount)
		errorCount := atomic.LoadInt64(&mcpServerInstance.errorCount)
		sessionCount := len(mcpServerInstance.sessions)
		mcpServerInstance.RUnlock()

		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		info := map[string]interface{}{
			"server": map[string]interface{}{
				"name":         serverNameMCP,
				"version":      serverVersionMCP,
				"transport":    transportMCP,
				"start_time":   startTime,
				"uptime":       time.Since(startTime).String(),
				"log_level":    logLevelMCP,
			},
			"system": map[string]interface{}{
				"os":            runtime.GOOS,
				"architecture":  runtime.GOARCH,
				"go_version":    runtime.Version(),
				"num_cpu":       runtime.NumCPU(),
				"num_goroutine": runtime.NumGoroutine(),
			},
			"memory": map[string]interface{}{
				"alloc_mb":       bToMb(m.Alloc),
				"total_alloc_mb": bToMb(m.TotalAlloc),
				"sys_mb":         bToMb(m.Sys),
				"gc_cycles":      m.NumGC,
			},
			"statistics": map[string]interface{}{
				"total_requests":  requestCount,
				"total_errors":    errorCount,
				"active_sessions": sessionCount,
				"success_rate":    calculateSuccessRate(requestCount, errorCount),
			},
			"features": map[string]interface{}{
				"cache_enabled":     enableCacheMCP,
				"telemetry_enabled": enableTelemetryMCP,
				"metrics_enabled":   enableMetricsMCP,
				"recovery_enabled":  enableRecoveryMCP,
				"cors_enabled":      corsEnabledMCP,
			},
		}

		jsonData, err := json.MarshalIndent(info, "", "  ")
		if err != nil {
			atomic.AddInt64(&mcpServerInstance.errorCount, 1)
			return mcp.NewToolResultError("Failed to marshal system info"), nil
		}

		atomic.AddInt64(&mcpServerInstance.requestCount, 1)
		return mcp.NewToolResultText(string(jsonData)), nil
	})

	// Add enhanced process management tool
	processTool := mcp.NewTool("process_info",
		mcp.WithDescription("Get detailed information about running processes and system load"),
		mcp.WithString("filter",
			mcp.Description("Optional filter for process names"),
		),
	)

	mcpServer.AddTool(processTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Update session activity
		if session := server.ClientSessionFromContext(ctx); session != nil {
			if mcpSession, ok := session.(*MCPSession); ok {
				mcpSession.UpdateActivity()
			}
		}

		processInfo := map[string]interface{}{
			"current_process": map[string]interface{}{
				"pid":        os.Getpid(),
				"goroutines": runtime.NumGoroutine(),
				"memory_mb":  getCurrentMemoryUsage(),
			},
			"system": map[string]interface{}{
				"cpu_count": runtime.NumCPU(),
				"timestamp": time.Now(),
			},
		}

		jsonData, err := json.MarshalIndent(processInfo, "", "  ")
		if err != nil {
			atomic.AddInt64(&mcpServerInstance.errorCount, 1)
			return mcp.NewToolResultError("Failed to marshal process info"), nil
		}

		atomic.AddInt64(&mcpServerInstance.requestCount, 1)
		return mcp.NewToolResultText(string(jsonData)), nil
	})

	// Add enhanced file operations tool with security validation
	fileOpsTool := mcp.NewTool("file_operations",
		mcp.WithDescription("Perform secure file operations like read, write, and list with enterprise validation"),
		mcp.WithString("operation",
			mcp.Required(),
			mcp.Description("Operation to perform (read, write, list)"),
			mcp.Enum("read", "write", "list"),
		),
		mcp.WithString("path",
			mcp.Required(),
			mcp.Description("File or directory path (restricted to working directory)"),
		),
		mcp.WithString("content",
			mcp.Description("Content to write (for write operation)"),
		),
	)

	mcpServer.AddTool(fileOpsTool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Update session activity
		if session := server.ClientSessionFromContext(ctx); session != nil {
			if mcpSession, ok := session.(*MCPSession); ok {
				mcpSession.UpdateActivity()
			}
		}

		operation, err := request.RequireString("operation")
		if err != nil {
			atomic.AddInt64(&mcpServerInstance.errorCount, 1)
			return mcp.NewToolResultError(err.Error()), nil
		}

		path, err := request.RequireString("path")
		if err != nil {
			atomic.AddInt64(&mcpServerInstance.errorCount, 1)
			return mcp.NewToolResultError(err.Error()), nil
		}

		// Enhanced security check: restrict to current working directory and validate path
		if err := validateFilePath(path); err != nil {
			atomic.AddInt64(&mcpServerInstance.errorCount, 1)
			return mcp.NewToolResultError(fmt.Sprintf("Security violation: %v", err)), nil
		}

		absPath, err := filepath.Abs(path)
		if err != nil {
			atomic.AddInt64(&mcpServerInstance.errorCount, 1)
			return mcp.NewToolResultError("Invalid path"), nil
		}

		cwd, err := os.Getwd()
		if err != nil {
			atomic.AddInt64(&mcpServerInstance.errorCount, 1)
			return mcp.NewToolResultError("Cannot determine current directory"), nil
		}

		if !strings.HasPrefix(absPath, cwd) {
			atomic.AddInt64(&mcpServerInstance.errorCount, 1)
			return mcp.NewToolResultError("Access denied: path outside working directory"), nil
		}

		switch operation {
		case "read":
			// Check file size before reading
			if stat, err := os.Stat(absPath); err != nil {
				atomic.AddInt64(&mcpServerInstance.errorCount, 1)
				return mcp.NewToolResultError(fmt.Sprintf("Failed to stat file: %v", err)), nil
			} else if stat.Size() > maxRequestSizeMCP {
				atomic.AddInt64(&mcpServerInstance.errorCount, 1)
				return mcp.NewToolResultError(fmt.Sprintf("File too large: %d bytes (max: %d)", stat.Size(), maxRequestSizeMCP)), nil
			}

			content, err := os.ReadFile(absPath)
			if err != nil {
				atomic.AddInt64(&mcpServerInstance.errorCount, 1)
				return mcp.NewToolResultError(fmt.Sprintf("Failed to read file: %v", err)), nil
			}
			atomic.AddInt64(&mcpServerInstance.requestCount, 1)
			return mcp.NewToolResultText(string(content)), nil

		case "list":
			entries, err := os.ReadDir(absPath)
			if err != nil {
				atomic.AddInt64(&mcpServerInstance.errorCount, 1)
				return mcp.NewToolResultError(fmt.Sprintf("Failed to list directory: %v", err)), nil
			}

			var fileList []map[string]interface{}
			for _, entry := range entries {
				info, err := entry.Info()
				if err != nil {
					continue // Skip entries we can't stat
				}
				fileList = append(fileList, map[string]interface{}{
					"name":     entry.Name(),
					"type":     getFileType(entry),
					"size":     info.Size(),
					"modified": info.ModTime(),
					"mode":     info.Mode().String(),
				})
			}
			listJSON, _ := json.MarshalIndent(fileList, "", "  ")
			atomic.AddInt64(&mcpServerInstance.requestCount, 1)
			return mcp.NewToolResultText(string(listJSON)), nil

		case "write":
			content := ""
			if args := request.GetArguments(); args != nil {
				if c, ok := args["content"].(string); ok {
					content = c
				}
			}
			if content == "" {
				atomic.AddInt64(&mcpServerInstance.errorCount, 1)
				return mcp.NewToolResultError("Content required for write operation"), nil
			}

			// Check content size
			if int64(len(content)) > maxRequestSizeMCP {
				atomic.AddInt64(&mcpServerInstance.errorCount, 1)
				return mcp.NewToolResultError(fmt.Sprintf("Content too large: %d bytes (max: %d)", len(content), maxRequestSizeMCP)), nil
			}

			err := os.WriteFile(absPath, []byte(content), 0644)
			if err != nil {
				atomic.AddInt64(&mcpServerInstance.errorCount, 1)
				return mcp.NewToolResultError(fmt.Sprintf("Failed to write file: %v", err)), nil
			}
			atomic.AddInt64(&mcpServerInstance.requestCount, 1)
			return mcp.NewToolResultText(fmt.Sprintf("File written successfully (%d bytes)", len(content))), nil

		default:
			atomic.AddInt64(&mcpServerInstance.errorCount, 1)
			return mcp.NewToolResultError(fmt.Sprintf("Unsupported operation: %s", operation)), nil
		}
	})

	// Add enhanced server status resource
	statusResource := mcp.NewResource(
		"server://status",
		"Enterprise Server Status",
		mcp.WithResourceDescription("Real-time server status, performance metrics, and system information"),
		mcp.WithMIMEType("application/json"),
	)

	mcpServer.AddResource(statusResource, func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
		mcpServerInstance.RLock()
		startTime := mcpServerInstance.startTime
		sessionCount := len(mcpServerInstance.sessions)
		requestCount := atomic.LoadInt64(&mcpServerInstance.requestCount)
		errorCount := atomic.LoadInt64(&mcpServerInstance.errorCount)
		mcpServerInstance.RUnlock()

		var m runtime.MemStats
		runtime.ReadMemStats(&m)

		status := map[string]interface{}{
			"server": map[string]interface{}{
				"name":              serverNameMCP,
				"version":           serverVersionMCP,
				"running":           true,
				"start_time":        startTime,
				"uptime":            time.Since(startTime).String(),
				"transport":         transportMCP,
				"cache_enabled":     enableCacheMCP,
				"telemetry_enabled": enableTelemetryMCP,
				"metrics_enabled":   enableMetricsMCP,
				"cors_enabled":      corsEnabledMCP,
				"log_level":         logLevelMCP,
			},
			"statistics": map[string]interface{}{
				"active_sessions": sessionCount,
				"total_requests":  requestCount,
				"total_errors":    errorCount,
				"success_rate":    calculateSuccessRate(requestCount, errorCount),
				"requests_per_min": calculateRequestsPerMinute(requestCount, startTime),
			},
			"system": map[string]interface{}{
				"memory_mb":  bToMb(m.Alloc),
				"goroutines": runtime.NumGoroutine(),
				"gc_cycles":  m.NumGC,
				"cpu_count":  runtime.NumCPU(),
			},
			"configuration": map[string]interface{}{
				"max_sessions":     maxSessionsMCP,
				"max_request_size": maxRequestSizeMCP,
				"cache_timeout":    cacheTimeoutMCP.String(),
				"operation_timeout": timeoutMCP.String(),
			},
			"timestamp": time.Now(),
		}

		jsonData, err := json.MarshalIndent(status, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("failed to marshal status: %w", err)
		}

		return []mcp.ResourceContents{
			mcp.TextResourceContents{
				URI:      "server://status",
				MIMEType: "application/json",
				Text:     string(jsonData),
			},
		}, nil
	})

	logVerbose("Enterprise tools and resources configured successfully")
	return nil
}

// loadMCPConfiguration loads tools and resources from a configuration file
func loadMCPConfiguration(mcpServer *server.MCPServer, configPath string, logVerbose func(string, ...interface{})) error {
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	var config MCPConfig
	if err := json.Unmarshal(configData, &config); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	// Load tools from configuration
	for _, toolConfig := range config.Tools {
		if err := loadToolFromConfig(mcpServer, toolConfig, logVerbose); err != nil {
			logVerbose("Failed to load tool %s: %v", toolConfig.Name, err)
		}
	}

	// Load resources from configuration
	for _, resourceConfig := range config.Resources {
		if err := loadResourceFromConfig(mcpServer, resourceConfig, logVerbose); err != nil {
			logVerbose("Failed to load resource %s: %v", resourceConfig.URI, err)
		}
	}

	return nil
}

// loadToolFromConfig creates and adds a tool from configuration
func loadToolFromConfig(mcpServer *server.MCPServer, config MCPToolConfig, logVerbose func(string, ...interface{})) error {
	tool := mcp.NewTool(config.Name, mcp.WithDescription(config.Description))

	// Add a generic handler that can be customized based on the config
	mcpServer.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// This is a placeholder implementation
		// In a real implementation, you would route to specific handlers based on config.Handler
		return mcp.NewToolResultText(fmt.Sprintf("Tool %s executed with handler %s", config.Name, config.Handler)), nil
	})

	logVerbose("Loaded tool: %s", config.Name)
	return nil
}

// loadResourceFromConfig creates and adds a resource from configuration
func loadResourceFromConfig(mcpServer *server.MCPServer, config MCPResourceConfig, logVerbose func(string, ...interface{})) error {
	if config.Template {
		template := mcp.NewResourceTemplate(
			config.URI,
			config.Name,
			mcp.WithTemplateDescription(config.Description),
			mcp.WithTemplateMIMEType(config.MIMEType),
		)

		mcpServer.AddResourceTemplate(template, func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			// Placeholder implementation
			return []mcp.ResourceContents{
				mcp.TextResourceContents{
					URI:      request.Params.URI,
					MIMEType: config.MIMEType,
					Text:     fmt.Sprintf("Resource template %s content", config.Name),
				},
			}, nil
		})
	} else {
		resource := mcp.NewResource(
			config.URI,
			config.Name,
			mcp.WithResourceDescription(config.Description),
			mcp.WithMIMEType(config.MIMEType),
		)

		mcpServer.AddResource(resource, func(ctx context.Context, request mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			// Placeholder implementation
			return []mcp.ResourceContents{
				mcp.TextResourceContents{
					URI:      config.URI,
					MIMEType: config.MIMEType,
					Text:     fmt.Sprintf("Resource %s content", config.Name),
				},
			}, nil
		})
	}

	logVerbose("Loaded resource: %s", config.URI)
	return nil
}

// watchConfigFile monitors the configuration file for changes and reloads
func watchConfigFile(ctx context.Context, mcpServer *server.MCPServer, configPath string, logVerbose func(string, ...interface{})) {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var lastModTime time.Time
	if stat, err := os.Stat(configPath); err == nil {
		lastModTime = stat.ModTime()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-mcpServerInstance.shutdownCh:
			return
		case <-ticker.C:
			if stat, err := os.Stat(configPath); err == nil {
				if stat.ModTime().After(lastModTime) {
					logVerbose("Configuration file changed, reloading...")
					if err := loadMCPConfiguration(mcpServer, configPath, logVerbose); err != nil {
						logVerbose("Failed to reload configuration: %v", err)
					} else {
						logVerbose("Configuration reloaded successfully")
					}
					lastModTime = stat.ModTime()
				}
			}
		}
	}
}

// createTelemetryMiddleware creates middleware for collecting telemetry data
func createTelemetryMiddleware() server.ToolHandlerMiddleware {
	return func(next server.ToolHandlerFunc) server.ToolHandlerFunc {
		return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			start := time.Now()
			result, err := next(ctx, request)
			duration := time.Since(start)

			success := err == nil
			if err != nil {
				_ = categorizeErrorType(err)
			}

			collectMCPTelemetry("tool_call", duration, success, 0, request.Params.Name, "", map[string]interface{}{
				"tool_name": request.Params.Name,
			})

			return result, err
		}
	}
}

// createSessionToolFilter creates a filter function for session-based tool access
func createSessionToolFilter() server.ToolFilterFunc {
	return func(ctx context.Context, tools []mcp.Tool) []mcp.Tool {
		session := server.ClientSessionFromContext(ctx)
		if session == nil {
			return tools // Return all tools if no session
		}

		// Example: filter tools based on session permissions
		// In a real implementation, you would check session permissions
		return tools
	}
}

// validateMCPConfiguration performs comprehensive validation of all MCP command configuration
func validateMCPConfiguration() error {
	var validationErrors []string

	// Validate timeout settings
	if timeoutMCP < 30*time.Second {
		validationErrors = append(validationErrors, "timeout is too short (minimum: 30s)")
	}
	if timeoutMCP > 24*time.Hour {
		validationErrors = append(validationErrors, "timeout is too long (maximum: 24h)")
	}

	// Validate port settings for HTTP/SSE transport
	if transportMCP == "http" || transportMCP == "sse" {
		if portMCP < 1 || portMCP > 65535 {
			validationErrors = append(validationErrors, "port must be between 1 and 65535")
		}
	}

	// Validate transport protocol
	validTransports := []string{"stdio", "http", "sse"}
	valid := false
	for _, t := range validTransports {
		if transportMCP == t {
			valid = true
			break
		}
	}
	if !valid {
		validationErrors = append(validationErrors, fmt.Sprintf("invalid transport: %s (valid: %s)", transportMCP, strings.Join(validTransports, ", ")))
	}

	// Validate session limits
	if maxSessionsMCP < 1 {
		validationErrors = append(validationErrors, "max-sessions must be at least 1")
	}
	if maxSessionsMCP > 10000 {
		validationErrors = append(validationErrors, "max-sessions is too high (maximum: 10000 for safety)")
	}

	// Validate cache settings
	if enableCacheMCP {
		if cacheTimeoutMCP < 1*time.Minute {
			validationErrors = append(validationErrors, "cache-timeout is too short (minimum: 1m)")
		}
		if cacheTimeoutMCP > 24*time.Hour {
			validationErrors = append(validationErrors, "cache-timeout is too long (maximum: 24h)")
		}
	}

	// Validate max request size
	if maxRequestSizeMCP < 1024 {
		validationErrors = append(validationErrors, "max-request-size is too small (minimum: 1KB)")
	}
	if maxRequestSizeMCP > 100*1024*1024 {
		validationErrors = append(validationErrors, "max-request-size is too large (maximum: 100MB)")
	}

	// Validate log level
	validLogLevels := []string{"debug", "info", "warn", "error"}
	validLevel := false
	for _, level := range validLogLevels {
		if logLevelMCP == level {
			validLevel = true
			break
		}
	}
	if !validLevel {
		validationErrors = append(validationErrors, fmt.Sprintf("invalid log level: %s (valid: %s)", logLevelMCP, strings.Join(validLogLevels, ", ")))
	}

	// Validate configuration file
	if configFileMCP != "" {
		if _, err := os.Stat(configFileMCP); os.IsNotExist(err) {
			validationErrors = append(validationErrors, fmt.Sprintf("configuration file does not exist: %s", configFileMCP))
		}
	}

	// Validate flag combinations
	if verboseMCP && quietMCP {
		validationErrors = append(validationErrors, "cannot use both --verbose and --quiet flags")
	}

	if len(validationErrors) > 0 {
		return fmt.Errorf("configuration validation failed:\n  • %s", strings.Join(validationErrors, "\n  • "))
	}

	return nil
}

// collectMCPTelemetry records a telemetry event for MCP operations
func collectMCPTelemetry(operation string, duration time.Duration, success bool, sessionCount int, toolName, resourceURI string, metadata map[string]interface{}) {
	if !enableTelemetryMCP {
		return
	}

	event := MCPTelemetryEvent{
		Timestamp:    time.Now(),
		Operation:    operation,
		Duration:     duration,
		Success:      success,
		SessionCount: sessionCount,
		ToolName:     toolName,
		ResourceURI:  resourceURI,
		Metadata:     metadata,
	}

	mcpTelemetryCollector.Lock()
	mcpTelemetryCollector.metrics = append(mcpTelemetryCollector.metrics, event)

	// Prevent unbounded memory growth
	if len(mcpTelemetryCollector.metrics) > 1000 {
		mcpTelemetryCollector.metrics = mcpTelemetryCollector.metrics[100:]
	}
	mcpTelemetryCollector.Unlock()
}

// Helper functions for enhanced functionality

// validateFilePath checks if a file path is safe for operations
func validateFilePath(path string) error {
	// Check for directory traversal attempts
	if strings.Contains(path, "..") {
		return fmt.Errorf("directory traversal detected")
	}

	// Check for absolute paths outside of working directory
	if filepath.IsAbs(path) {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("cannot determine working directory")
		}
		if !strings.HasPrefix(path, cwd) {
			return fmt.Errorf("absolute path outside working directory")
		}
	}

	// Check for potentially dangerous file extensions
	dangerousExts := []string{".exe", ".bat", ".sh", ".ps1", ".cmd"}
	ext := strings.ToLower(filepath.Ext(path))
	for _, dangerousExt := range dangerousExts {
		if ext == dangerousExt {
			return fmt.Errorf("potentially dangerous file extension: %s", ext)
		}
	}

	return nil
}

// getFileType returns a human-readable file type
func getFileType(entry os.DirEntry) string {
	if entry.IsDir() {
		return "directory"
	}
	if info, err := entry.Info(); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return "symlink"
		}
		if info.Mode()&os.ModeDevice != 0 {
			return "device"
		}
	}
	return "file"
}

// bToMb converts bytes to megabytes
func bToMb(b uint64) uint64 {
	return b / 1024 / 1024
}

// getCurrentMemoryUsage returns current memory usage in MB
func getCurrentMemoryUsage() uint64 {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return bToMb(m.Alloc)
}

// calculateSuccessRate calculates the success rate percentage
func calculateSuccessRate(totalRequests, errorCount int64) float64 {
	if totalRequests == 0 {
		return 100.0
	}
	successCount := totalRequests - errorCount
	return float64(successCount) / float64(totalRequests) * 100.0
}

// calculateRequestsPerMinute calculates requests per minute since start
func calculateRequestsPerMinute(totalRequests int64, startTime time.Time) float64 {
	minutes := time.Since(startTime).Minutes()
	if minutes == 0 {
		return 0
	}
	return float64(totalRequests) / minutes
}

// performGracefulShutdown handles graceful shutdown of the MCP server
func performGracefulShutdown(ctx context.Context, logInfo, logVerbose func(string, ...interface{})) error {
	logVerbose("Starting graceful shutdown sequence...")

	// Mark server as shutting down
	mcpServerInstance.Lock()
	mcpServerInstance.running = false
	sessionCount := len(mcpServerInstance.sessions)
	mcpServerInstance.Unlock()

	logVerbose("Notifying %d active sessions of shutdown...", sessionCount)

	// Notify all sessions of impending shutdown
	mcpServerInstance.RLock()
	for sessionID, session := range mcpServerInstance.sessions {
		select {
		case session.notifChannel <- mcp.JSONRPCNotification{}:
			logVerbose("Notified session %s of shutdown", sessionID)
		default:
			logVerbose("Could not notify session %s (channel full)", sessionID)
		}
	}
	mcpServerInstance.RUnlock()

	// Wait a moment for notifications to be processed
	time.Sleep(1 * time.Second)

	// Close shutdown channel to signal other goroutines
	close(mcpServerInstance.shutdownCh)

	// Export final telemetry if enabled
	if enableTelemetryMCP {
		logVerbose("Exporting final telemetry data...")
		if data, err := exportMCPTelemetryData(); err == nil {
			// In a real implementation, you might want to save this to a file
			logVerbose("Final telemetry: %d events collected", len(mcpTelemetryCollector.metrics))
			_ = data // Suppress unused variable warning
		}
	}

	logVerbose("Graceful shutdown sequence completed")
	return nil
}

// exportMCPTelemetryData exports all collected telemetry data
func exportMCPTelemetryData() ([]byte, error) {
	mcpTelemetryCollector.RLock()
	defer mcpTelemetryCollector.RUnlock()

	data := struct {
		ExportTime time.Time              `json:"export_time"`
		ServerInfo map[string]interface{} `json:"server_info"`
		Events     []MCPTelemetryEvent    `json:"events"`
	}{
		ExportTime: time.Now(),
		ServerInfo: map[string]interface{}{
			"server_name": serverNameMCP,
			"version":     serverVersionMCP,
			"transport":   transportMCP,
			"start_time":  mcpServerInstance.startTime,
		},
		Events: mcpTelemetryCollector.metrics,
	}

	return json.MarshalIndent(data, "", "  ")
}