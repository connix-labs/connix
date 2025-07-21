# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Development Commands

All commands should be run using the Nix development environment:

### Core Commands
- `nix develop -c go build -o connix .` - Build the CLI binary
- `nix develop -c go run main.go <command>` - Run the CLI directly
- `nix develop -c go test ./...` - Run all unit tests
- `nix develop -c go test -tags=integration ./...` - Run integration tests (requires git repository)
- `nix develop -c go test -run TestSpecificFunction ./cmd` - Run specific test function

### Code Quality
- `nix develop -c golangci-lint run` - Run comprehensive linting
- `nix develop -c revive ./...` - Additional Go style checking
- `nix develop -c go fmt ./...` - Format Go code
- `nix develop -c go vet ./...` - Go static analysis
- `nix develop -c go mod tidy` - Clean up module dependencies

### Development Tools
- `nix develop -c air` - Hot reload during development
- `nix develop -c gomarkdoc .` - Generate documentation from comments
- `nix develop -c dx` - Quick edit flake.nix
- `nix develop -c gx` - Quick edit go.mod

### Nix Environment
- `nix flake check` - Validate flake configuration
- `nix develop -c alejandra .` - Format Nix files
- `nix develop --cores=4 --max-jobs=4` - Build with resource limits

## Architecture Overview

This is a sophisticated Go CLI application called **Connix** - a container platform and MCP (Model Context Protocol) server management tool. The architecture follows modern Go patterns with a Cobra-based command structure.

### Core Components

#### MCP Server (`cmd/mcp.go`)
The centerpiece is a high-performance MCP server that supports multiple transport protocols:
- **Multi-transport**: stdio, HTTP, SSE protocols with automatic transport detection
- **Session Management**: Concurrent handling of up to 100 sessions with per-session customization
- **Tool System**: Extensible tool registration with file operations, system info, and custom tools
- **Caching**: Multi-level caching with read-write locks for performance optimization
- **Configuration**: Hot-reload JSON configuration files with environment variable overrides

Key MCP patterns:
```go
// MCP server follows this registration pattern
server := mcp.NewMCPServer("connix", "1.0.0")
server.RegisterTool("tool_name", toolHandler)
```

#### Git Synchronization (`cmd/sync.go`)
Advanced git branch management with enterprise-grade features:
- **Worktree Detection**: Automatic git repository root discovery
- **Concurrent Processing**: Bounded concurrency with configurable limits (default: 5 branches)
- **Caching System**: Repository state checksums with intelligent cache invalidation
- **Error Recovery**: Categorized error types (network, corruption, permission) with specific recovery strategies
- **Telemetry**: Performance tracking and analysis

#### Hooks Management (`cmd/hooks.go`)
Integration with Claude Code's Python hook system:
- **Hook Types**: pre_tool_use, post_tool_use, user_prompt_submit, notification, stop, subagent_stop
- **Status Monitoring**: Executable permission checking and validation
- **Log Analysis**: JSON log parsing and display
- **Testing**: Synthetic test data generation for hook validation

### Command Structure

Built on Cobra CLI framework with this hierarchy:
```
connix
├── mcp [stdio|http|sse]        # Start MCP server
├── sync [flags]                # Sync git branches
├── hooks                       # List all hooks
│   ├── <hook-name>            # Manage specific hook
│   │   ├── test               # Test hook execution
│   │   └── logs               # View hook logs
```

### Configuration Management

Multi-layer configuration system:
1. **Environment Variables**: All flags support env var overrides (e.g., `CONNIX_MCP_PORT`)
2. **Command Flags**: Cobra-based CLI flags with validation
3. **Configuration Files**: JSON configuration for MCP server with hot reload
4. **Defaults**: Sensible defaults for production use

### Error Handling Patterns

The codebase implements sophisticated error handling:

```go
// Categorized errors with recovery suggestions
if err := gitOperation(); err != nil {
    return categorizeGitError(err)
}

// User-friendly error messages with troubleshooting
return fmt.Errorf("❌ MCP server failed: %w\n\nTip: Check port availability", err)
```

### Performance Optimizations

#### Concurrency Patterns
- **Semaphore-based**: Bounded concurrency for git operations
- **Context-aware**: Proper timeout and cancellation handling
- **Thread-safe**: Mutex protection for shared resources

#### Caching Strategy
- **Repository Caching**: Git state checksums for branch sync
- **MCP Caching**: Session-based caching with TTL
- **Memory Management**: Bounded collections to prevent memory leaks

### Testing Architecture

Comprehensive testing strategy with clear separation:

#### Unit Tests (`*_test.go`)
- Environment variable parsing validation
- Core business logic testing
- Error handling verification

#### Integration Tests (`*_integration_test.go`)
- Real git repository operations
- Network-dependent functionality
- Build tag: `//go:build integration`

### Dependencies and External Integration

#### Core Dependencies
- `github.com/mark3labs/mcp-go v0.34.1` - MCP protocol implementation
- `github.com/spf13/cobra v1.9.1` - CLI framework
- `github.com/google/uuid` - Session UUID generation

#### Claude Code Integration
The CLI deeply integrates with Claude Code through:
- **Hook System**: Python-based lifecycle hooks in `.claude/hooks/`
- **Log Management**: Structured JSON logging in `logs/` directory
- **Session Tracking**: MCP session correlation with Claude Code workflows

### Development Environment

#### Nix Flake Configuration
The project uses Nix flakes for reproducible development:
- **Go 1.24**: Latest stable Go toolchain
- **Development Tools**: golangci-lint, revive, air, gopls
- **Documentation**: gomarkdoc for automated documentation
- **Profiling**: pprof and graphviz for performance analysis

#### Environment Setup
```bash
# Enter development shell
nix develop

# With resource limits (recommended)
nix develop --cores=4 --max-jobs=4
```

## Key Implementation Patterns

### MCP Server Development
When extending MCP functionality:
- Tools must implement proper JSON-RPC request/response patterns
- Use the existing tool registration pattern in `runMCP()`
- Implement proper error handling with user-friendly messages
- Consider caching for expensive operations

### Command Implementation
New commands should follow the established pattern:
- Create command file in `cmd/` directory
- Follow Cobra command structure from existing commands
- Implement proper flag validation and environment variable support
- Add comprehensive error handling with recovery suggestions
- Include telemetry collection for performance monitoring

### Git Operations
When working with git functionality:
- Use the existing worktree detection pattern
- Implement proper error categorization
- Add retry mechanisms for network operations
- Consider caching for expensive git operations

### Hook Integration
Claude Code hooks follow this pattern:
- Python scripts in `.claude/hooks/` directory
- JSON-based input/output with structured logging
- Exit code conventions: 0 = allow, 2 = block with error
- Log files stored in `logs/` directory with hook name

## Important Notes

- **Nix Environment**: Always use `nix develop -c <command>` for consistent tooling
- **Resource Limits**: Build with `--cores=4 --max-jobs=4` to prevent system overload
- **Integration Tests**: Require real git repository and network access
- **MCP Sessions**: Server supports up to 100 concurrent sessions
- **Hook Safety**: Pre-tool-use hooks can block dangerous operations
- **Caching**: Multiple caching layers for performance optimization
- **Error Recovery**: Comprehensive error categorization with specific recovery strategies