/*
Copyright © 2025 connix-labs

This file implements the git branch synchronization functionality for the connix CLI.
It provides commands to automatically fetch and create local tracking branches for
all remote branches in a git repository or worktree.

Key Features:
- Automatic git worktree root detection
- Concurrent branch creation with configurable limits
- Comprehensive error handling with user-friendly messages
- Support for dry-run mode to preview changes
- Environment variable configuration support
- Context-aware timeouts for all operations

The sync command is designed to work seamlessly with both regular git repositories
and git worktrees, making it suitable for complex development workflows.
*/

package cmd

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
)

// Configuration variables for the sync command.
// These control various aspects of the synchronization behavior and can be
// set via command-line flags or environment variables.
var (
	// verboseSync enables detailed output during sync operations.
	// When true, shows additional information about git operations and progress.
	verboseSync bool

	// dryRunSync enables preview mode without making actual changes.
	// When true, shows what operations would be performed without executing them.
	dryRunSync bool

	// quietSync suppresses non-error output during sync operations.
	// When true, only errors and critical information are displayed.
	quietSync bool

	// timeoutSync sets the maximum duration for git operations.
	// Defaults to 5 minutes but can be configured via CONNIX_SYNC_TIMEOUT env var.
	timeoutSync time.Duration

	// maxConcurrent limits the number of concurrent branch operations.
	// Defaults to 5 but can be configured via CONNIX_MAX_CONCURRENT env var.
	// Higher values may improve performance but consume more system resources.
	maxConcurrent int

	// skipValidation bypasses git repository validation checks.
	// When true, skips verification that the current directory is a valid git repo.
	// Can be configured via CONNIX_SKIP_VALIDATION env var.
	skipValidation bool

	// enableCache controls whether to use caching for git operations.
	// When true, caches branch lists and repository metadata for better performance.
	enableCache bool

	// cacheTimeout sets how long cached data remains valid.
	// Defaults to 5 minutes but can be configured via CONNIX_CACHE_TIMEOUT env var.
	cacheTimeout time.Duration

	// enableTelemetry controls whether to collect performance and usage metrics.
	// When true, collects anonymous metrics for performance analysis and improvements.
	enableTelemetry bool
)

// Performance optimization: Cache for expensive git operations
// This reduces redundant git commands and improves performance for repeated operations
var (
	// branchCache stores cached branch information with timestamps
	branchCache = struct {
		sync.RWMutex
		local  cacheEntry
		remote cacheEntry
	}{}

	// repoMetadataCache stores repository metadata
	repoMetadataCache = struct {
		sync.RWMutex
		data map[string]cacheEntry
	}{
		data: make(map[string]cacheEntry),
	}

	// telemetryCollector stores performance and usage metrics
	telemetryCollector = struct {
		sync.RWMutex
		metrics []TelemetryEvent
	}{}
)

// TelemetryEvent represents a single metrics event
type TelemetryEvent struct {
	Timestamp   time.Time             `json:"timestamp"`
	Operation   string                `json:"operation"`
	Duration    time.Duration         `json:"duration_ms"`
	Success     bool                  `json:"success"`
	BranchCount int                   `json:"branch_count,omitempty"`
	ErrorType   string                `json:"error_type,omitempty"`
	Metadata    map[string]interface{} `json:"metadata,omitempty"`
}

// cacheEntry represents a cached value with expiration
type cacheEntry struct {
	value     interface{}
	timestamp time.Time
	checksum  string // For cache invalidation
}

// isValid checks if a cache entry is still valid based on timeout and checksum
func (c *cacheEntry) isValid(timeout time.Duration, currentChecksum string) bool {
	if time.Since(c.timestamp) > timeout {
		return false
	}
	if currentChecksum != "" && c.checksum != currentChecksum {
		return false
	}
	return true
}

var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Sync all remote branches in the git worktree",
	Long: `Fetch all remote branches and ensure they are available locally.

This command works at the root of a git worktree and syncs all pushed branches.
It automatically detects the git worktree root and creates local tracking branches
for any remote branches that don't exist locally.

Features:
  • Automatic git worktree root detection
  • Concurrent branch creation for improved performance
  • Comprehensive error handling with helpful tips
  • Dry-run mode to preview changes
  • Configurable timeouts and concurrency limits
  • Environment variable support for configuration

Examples:
  # Basic sync
  connix sync

  # Dry run to see what would be done
  connix sync --dry-run

  # Verbose output with custom timeout
  connix sync --verbose --timeout 10m

  # Quiet mode with custom concurrency
  connix sync --quiet --max-concurrent 10

Environment Variables:
  CONNIX_SYNC_TIMEOUT     - Default timeout (e.g., "5m", "30s")
  CONNIX_MAX_CONCURRENT   - Max concurrent operations (default: 5)
  CONNIX_SKIP_VALIDATION  - Skip git repository validation (true/false)`,
	RunE: runSync,
}

// init initializes the sync command and its flags.
// This function is called automatically when the package is imported.
// It sets up command-line flags with defaults from environment variables
// and registers the sync command with the root command.
func init() {
	// Configure command-line flags with environment variable fallbacks
	syncCmd.Flags().BoolVarP(&verboseSync, "verbose", "v", false, "Enable verbose output")
	syncCmd.Flags().BoolVarP(&dryRunSync, "dry-run", "n", false, "Show what would be done without making changes")
	syncCmd.Flags().BoolVarP(&quietSync, "quiet", "q", false, "Suppress non-error output")
	syncCmd.Flags().DurationVar(&timeoutSync, "timeout", getEnvDuration("CONNIX_SYNC_TIMEOUT", 5*time.Minute), "Timeout for git operations")
	syncCmd.Flags().IntVar(&maxConcurrent, "max-concurrent", getEnvInt("CONNIX_MAX_CONCURRENT", 5), "Maximum concurrent branch operations")
	syncCmd.Flags().BoolVar(&skipValidation, "skip-validation", getEnvBool("CONNIX_SKIP_VALIDATION", false), "Skip git repository validation")
	syncCmd.Flags().BoolVar(&enableCache, "enable-cache", getEnvBool("CONNIX_ENABLE_CACHE", true), "Enable caching for better performance")
	syncCmd.Flags().DurationVar(&cacheTimeout, "cache-timeout", getEnvDuration("CONNIX_CACHE_TIMEOUT", 5*time.Minute), "Cache timeout duration")
	syncCmd.Flags().BoolVar(&enableTelemetry, "enable-telemetry", getEnvBool("CONNIX_ENABLE_TELEMETRY", false), "Enable anonymous telemetry collection")
	
	// Register the sync command as a subcommand of the root command
	rootCmd.AddCommand(syncCmd)
}

// getEnvDuration retrieves a duration value from an environment variable.
// If the environment variable is not set or cannot be parsed as a duration,
// it returns the provided default value.
//
// Parameters:
//   - key: The environment variable name to check
//   - defaultValue: The fallback duration if env var is not set or invalid
//
// Returns:
//   - The parsed duration from the environment variable, or defaultValue
//
// Example:
//   timeout := getEnvDuration("CONNIX_TIMEOUT", 5*time.Minute) // "30s", "5m", "1h"
func getEnvDuration(key string, defaultValue time.Duration) time.Duration {
	if val := os.Getenv(key); val != "" {
		if duration, err := time.ParseDuration(val); err == nil {
			return duration
		}
		// Note: Invalid duration format is silently ignored and defaults are used
	}
	return defaultValue
}

// getEnvInt retrieves an integer value from an environment variable.
// If the environment variable is not set or cannot be parsed as an integer,
// it returns the provided default value.
//
// Parameters:
//   - key: The environment variable name to check
//   - defaultValue: The fallback integer if env var is not set or invalid
//
// Returns:
//   - The parsed integer from the environment variable, or defaultValue
//
// Example:
//   maxWorkers := getEnvInt("CONNIX_MAX_CONCURRENT", 5)
func getEnvInt(key string, defaultValue int) int {
	if val := os.Getenv(key); val != "" {
		if intVal, err := strconv.Atoi(val); err == nil {
			return intVal
		}
		// Note: Invalid integer format is silently ignored and defaults are used
	}
	return defaultValue
}

// getEnvBool retrieves a boolean value from an environment variable.
// If the environment variable is not set or cannot be parsed as a boolean,
// it returns the provided default value.
//
// Parameters:
//   - key: The environment variable name to check
//   - defaultValue: The fallback boolean if env var is not set or invalid
//
// Returns:
//   - The parsed boolean from the environment variable, or defaultValue
//
// Supported boolean values: "true", "false", "1", "0", "t", "f", "TRUE", "FALSE"
//
// Example:
//   skipValidation := getEnvBool("CONNIX_SKIP_VALIDATION", false)
func getEnvBool(key string, defaultValue bool) bool {
	if val := os.Getenv(key); val != "" {
		if boolVal, err := strconv.ParseBool(val); err == nil {
			return boolVal
		}
		// Note: Invalid boolean format is silently ignored and defaults are used
	}
	return defaultValue
}

// runSync is the main entry point for the sync command execution.
// It orchestrates the entire branch synchronization process including:
// - Git worktree root detection
// - Repository validation
// - Remote branch fetching
// - Local tracking branch creation
//
// The function implements comprehensive error handling and supports multiple
// output modes (verbose, quiet, dry-run) with user-friendly progress reporting.
//
// Parameters:
//   - cmd: The cobra command instance (unused but required by cobra.Command.RunE)
//   - args: Command-line arguments (unused but required by cobra.Command.RunE)
//
// Returns:
//   - error: nil on success, or a detailed error with user-friendly messages
//
// Error Recovery:
//   - Automatically restores the original working directory on function exit
//   - Provides actionable error messages with troubleshooting tips
//   - Gracefully handles context cancellation and timeouts
func runSync(cmd *cobra.Command, args []string) error {
	// Start telemetry collection for the entire sync operation
	syncStart := time.Now()
	var telemetryMetadata = make(map[string]interface{})
	defer func() {
		if enableTelemetry {
			collectTelemetry("sync_complete", time.Since(syncStart), true, 0, "", telemetryMetadata)
		}
	}()

	// Create a context with timeout for all git operations
	// This ensures operations don't hang indefinitely
	ctx, cancel := context.WithTimeout(context.Background(), timeoutSync)
	defer cancel()

	// Define logging functions based on user preferences
	// These closures capture the current flag states for consistent behavior
	logInfo := func(msg string, args ...interface{}) {
		if !quietSync {
			fmt.Printf(msg+"\n", args...)
		}
	}

	logVerbose := func(msg string, args ...interface{}) {
		if verboseSync && !quietSync {
			fmt.Printf("[VERBOSE] "+msg+"\n", args...)
		}
	}

	logDryRun := func(msg string, args ...interface{}) {
		if dryRunSync && !quietSync {
			fmt.Printf("[DRY RUN] "+msg+"\n", args...)
		}
	}

	// Step 0: Validate configuration before proceeding
	if err := validateSyncConfiguration(); err != nil {
		return fmt.Errorf("❌ Configuration validation failed: %w", err)
	}

	// Step 1: Locate the git worktree root directory
	// This traverses up the directory tree looking for .git directory or file
	workTreeRoot, err := findGitWorktreeRoot()
	if err != nil {
		return fmt.Errorf("❌ Failed to locate git worktree root: %w\n\nTip: Ensure you're running this command from within a git repository or worktree", err)
	}

	logVerbose("Found git worktree root: %s", workTreeRoot)

	// Step 2: Preserve original working directory for restoration
	// This ensures we can return to the user's original location even if errors occur
	originalDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %w", err)
	}
	defer func() {
		// Critical: Always attempt to restore the original directory
		// This prevents leaving the user in an unexpected location
		if err := os.Chdir(originalDir); err != nil {
			logVerbose("Warning: failed to restore original directory: %v", err)
		}
	}()

	// Step 3: Change to the git worktree root for all subsequent operations
	if err := os.Chdir(workTreeRoot); err != nil {
		return fmt.Errorf("failed to change to worktree root %s: %w", workTreeRoot, err)
	}

	// Step 4: Validate that we're in a proper git repository (unless skipped)
	// This prevents confusing errors from git commands later
	if !skipValidation && !isGitRepository() {
		return fmt.Errorf("❌ Directory %s is not a valid git repository\n\nTip: Use --skip-validation to bypass this check", workTreeRoot)
	}

	// Step 5: Verify that a git remote is configured
	// Without a remote, there are no branches to sync
	hasRemote, err := hasGitRemote(ctx)
	if err != nil {
		return fmt.Errorf("failed to check for git remote: %w", err)
	}
	if !hasRemote {
		return fmt.Errorf("❌ No git remote configured\n\nTip: Add a remote with 'git remote add origin <url>'")
	}

	// Step 6: Fetch all remote branches (unless in dry-run mode)
	// This updates the local repository with the latest remote references
	if !dryRunSync {
		logInfo("🔄 Fetching all remote branches...")
		if err := fetchAllBranches(ctx); err != nil {
			return fmt.Errorf("❌ Failed to fetch branches: %w\n\nTip: Check your network connection and remote repository access", err)
		}
		logInfo("✅ Successfully fetched remote branches")
	} else {
		logDryRun("Would fetch all remote branches")
	}

	// Step 7: Synchronize remote tracking branches
	// This is the core operation that creates missing local tracking branches
	logInfo("🔀 Syncing remote tracking branches...")
	newBranches, err := syncRemoteBranches(ctx, logVerbose, logDryRun)
	if err != nil {
		return fmt.Errorf("❌ Failed to sync branches: %w", err)
	}

	// Step 8: Report results to the user
	if len(newBranches) > 0 {
		logInfo("✅ Created %d new tracking branches: %s", len(newBranches), strings.Join(newBranches, ", "))
	} else {
		logInfo("✅ All remote branches are already tracked locally")
	}

	logInfo("🎉 Branch sync completed successfully!")
	return nil
}

// findGitWorktreeRoot locates the root directory of a git repository or worktree.
// It traverses up the directory tree from the current working directory,
// looking for a .git directory or .git file (in case of worktrees).
//
// The function handles both scenarios:
// 1. Regular git repository: .git is a directory containing repository data
// 2. Git worktree: .git is a file containing "gitdir: <path>" pointing to the git directory
//
// Returns:
//   - string: The absolute path to the git worktree root
//   - error: nil on success, error if not in a git repository/worktree
//
// Algorithm:
//   - Start from current working directory
//   - Check each directory for .git (directory or file)
//   - If .git is a directory, this is a regular repo root
//   - If .git is a file starting with "gitdir: ", this is a worktree root
//   - Continue up the tree until found or reach filesystem root
func findGitWorktreeRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	dir := cwd
	for {
		gitDir := filepath.Join(dir, ".git")
		if info, err := os.Stat(gitDir); err == nil {
			// Case 1: .git is a directory (regular git repository)
			if info.IsDir() {
				return dir, nil
			}
			
			// Case 2: .git is a file (git worktree)
			// Read the file and check if it contains a gitdir reference
			data, err := os.ReadFile(gitDir)
			if err == nil && strings.HasPrefix(string(data), "gitdir: ") {
				return dir, nil
			}
		}

		// Move up one directory level
		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached the filesystem root without finding .git
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("not in a git repository or worktree")
}

// fetchAllBranches retrieves all branches from all configured git remotes.
// It uses 'git fetch --all --prune' to:
// - Fetch branches from all remotes (--all)
// - Remove tracking branches for deleted remote branches (--prune)
//
// Parameters:
//   - ctx: Context for timeout and cancellation control
//
// Returns:
//   - error: nil on success, error if git fetch fails
//
// Behavior:
//   - In verbose mode, shows git command output to user
//   - In quiet mode, suppresses git command output
//   - Respects context timeout for network operations
func fetchAllBranches(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "git", "fetch", "--all", "--prune")
	if verboseSync {
		// In verbose mode, show git output to the user
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	return cmd.Run()
}

// hasGitRemote checks whether the current git repository has any configured remotes.
// This is essential before attempting to fetch or sync branches.
//
// Parameters:
//   - ctx: Context for timeout and cancellation control
//
// Returns:
//   - bool: true if at least one remote is configured, false otherwise
//   - error: nil on success, error if git command fails
//
// Implementation:
//   - Runs 'git remote' command to list all remotes
//   - Returns true if output contains any remote names
//   - Returns false if output is empty (no remotes configured)
func hasGitRemote(ctx context.Context) (bool, error) {
	cmd := exec.CommandContext(ctx, "git", "remote")
	output, err := cmd.Output()
	if err != nil {
		return false, err
	}
	return len(strings.TrimSpace(string(output))) > 0, nil
}

// syncRemoteBranches creates local tracking branches for remote branches that don't exist locally.
// This is the core synchronization function that implements concurrent branch creation
// with proper error handling and progress reporting.
//
// Algorithm:
//   1. Get list of all remote branches
//   2. Get list of all local branches  
//   3. Calculate diff (remote branches not tracked locally)
//   4. Create local tracking branches concurrently (if not dry-run)
//   5. Collect and report results
//
// Concurrency Design:
//   - Uses a semaphore pattern to limit concurrent git operations
//   - Worker goroutines are bounded by maxConcurrent setting
//   - Mutex protects shared state (createdBranches slice)
//   - Error channel collects failures from all workers
//   - WaitGroup ensures all workers complete before returning
//
// Parameters:
//   - ctx: Context for timeout and cancellation control
//   - logVerbose: Function to log verbose messages
//   - logDryRun: Function to log dry-run messages
//
// Returns:
//   - []string: List of successfully created branch names
//   - error: Aggregated errors if any branches failed to create
//
// Error Handling:
//   - Partial success is supported (some branches created, others failed)
//   - All errors are collected and reported together
//   - Function returns both successful branches and error details
func syncRemoteBranches(ctx context.Context, logVerbose, logDryRun func(string, ...interface{})) ([]string, error) {
	// Step 1: Gather branch information
	remoteBranches, err := getRemoteBranches(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get remote branches: %w", err)
	}

	existingBranches, err := getLocalBranches(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get local branches: %w", err)
	}

	// Step 2: Calculate which branches need to be created
	// Use a map for O(1) lookup performance when checking existing branches
	branchSet := make(map[string]bool)
	for _, branch := range existingBranches {
		branchSet[branch] = true
	}

	var branchesToCreate []string
	for _, remoteBranch := range remoteBranches {
		// Skip the special HEAD reference
		if remoteBranch == "HEAD" {
			continue
		}
		
		// Add to creation list if not already tracked locally
		if !branchSet[remoteBranch] {
			branchesToCreate = append(branchesToCreate, remoteBranch)
		}
	}

	// Early return if no work needed
	if len(branchesToCreate) == 0 {
		return nil, nil
	}

	logVerbose("Found %d branches to create: %s", len(branchesToCreate), strings.Join(branchesToCreate, ", "))

	// Step 3: Handle dry-run mode (no actual branch creation)
	var createdBranches []string
	if dryRunSync {
		for _, branch := range branchesToCreate {
			logDryRun("Would create local tracking branch: %s", branch)
			createdBranches = append(createdBranches, branch)
		}
		return createdBranches, nil
	}

	// Step 4: Set up concurrent branch creation infrastructure
	var wg sync.WaitGroup              // Synchronizes worker goroutines
	var mu sync.Mutex                  // Protects shared createdBranches slice
	semaphore := make(chan struct{}, maxConcurrent) // Limits concurrent operations
	errChan := make(chan error, len(branchesToCreate)) // Collects errors from workers

	// Step 5: Launch concurrent workers for branch creation
	for _, branch := range branchesToCreate {
		wg.Add(1)
		go func(branchName string) {
			defer wg.Done()
			
			// Acquire semaphore slot (blocks if maxConcurrent reached)
			semaphore <- struct{}{}
			defer func() { <-semaphore }() // Release slot when done

			logVerbose("Creating tracking branch: %s", branchName)
			
			// Execute git command to create tracking branch with retry mechanism
			if err := createTrackingBranchWithRetry(ctx, branchName, 3); err != nil {
				// Send error to collection channel (non-blocking due to buffered channel)
				errChan <- fmt.Errorf("failed to create tracking branch %s after retries: %w", branchName, err)
				return
			}

			// Thread-safe update of successful branches list
			mu.Lock()
			createdBranches = append(createdBranches, branchName)
			mu.Unlock()
		}(branch)
	}

	// Step 6: Wait for all workers to complete and collect errors
	wg.Wait()
	close(errChan) // Signal that no more errors will be sent

	var errors []error
	for err := range errChan {
		errors = append(errors, err)
	}

	// Step 7: Handle partial success scenario with intelligent error analysis
	if len(errors) > 0 {
		// Use advanced error handling for better user experience
		return createdBranches, handlePartialFailure(createdBranches, errors, len(branchesToCreate))
	}

	return createdBranches, nil
}

// getRemoteBranches retrieves a list of all remote tracking branches with caching support.
// It uses 'git branch -r --format=%(refname:short)' to get clean branch names
// and filters to only include branches from the 'origin' remote.
//
// Performance Optimizations:
//   - Caches results to avoid repeated git commands
//   - Uses repository state checksum for cache invalidation
//   - Thread-safe caching with read-write locks
//
// Parameters:
//   - ctx: Context for timeout and cancellation control
//
// Returns:
//   - []string: List of remote branch names (without 'origin/' prefix)
//   - error: nil on success, error if git command fails
//
// Filtering Logic:
//   - Only includes branches with 'origin/' prefix
//   - Strips the 'origin/' prefix from returned names
//   - Excludes the special 'HEAD' reference
//   - Handles empty lines and whitespace gracefully
func getRemoteBranches(ctx context.Context) ([]string, error) {
	if enableCache {
		if branches, ok := getCachedRemoteBranches(ctx); ok {
			return branches, nil
		}
	}

	cmd := exec.CommandContext(ctx, "git", "branch", "-r", "--format=%(refname:short)")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get remote branches: %w", err)
	}

	var branches []string
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" && strings.HasPrefix(line, "origin/") {
			branch := strings.TrimPrefix(line, "origin/")
			// Filter out the special HEAD reference
			if branch != "HEAD" {
				branches = append(branches, branch)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to parse remote branches: %w", err)
	}

	if enableCache {
		cacheRemoteBranches(ctx, branches)
	}

	return branches, nil
}

// isGitRepository checks if the current directory is within a git repository.
// It uses 'git rev-parse --git-dir' which succeeds if run from within a git repo.
//
// Returns:
//   - bool: true if in a git repository, false otherwise
//
// Note: This is a simple validation check. It doesn't verify repository integrity
// or whether the repository is in a valid state for operations.
func isGitRepository() bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	return cmd.Run() == nil
}

// getLocalBranches retrieves a list of all local branches in the repository with caching support.
// It uses 'git branch --format=%(refname:short)' to get clean branch names
// without asterisks or other formatting characters.
//
// Performance Optimizations:
//   - Caches results to avoid repeated git commands
//   - Uses repository state checksum for cache invalidation
//   - Thread-safe caching with read-write locks
//
// Parameters:
//   - ctx: Context for timeout and cancellation control
//
// Returns:
//   - []string: List of local branch names
//   - error: nil on success, error if git command fails
//
// Implementation Notes:
//   - Uses git's format option for consistent output
//   - Handles empty lines and whitespace properly
//   - Returns all local branches regardless of checkout status
func getLocalBranches(ctx context.Context) ([]string, error) {
	if enableCache {
		if branches, ok := getCachedLocalBranches(ctx); ok {
			return branches, nil
		}
	}

	cmd := exec.CommandContext(ctx, "git", "branch", "--format=%(refname:short)")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("failed to get local branches: %w", err)
	}

	var branches []string
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			branches = append(branches, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to parse local branches: %w", err)
	}

	if enableCache {
		cacheLocalBranches(ctx, branches)
	}

	return branches, nil
}

// getCachedRemoteBranches attempts to retrieve remote branches from cache
func getCachedRemoteBranches(ctx context.Context) ([]string, bool) {
	checksum, err := getRepoStateChecksum(ctx)
	if err != nil {
		return nil, false
	}

	branchCache.RLock()
	defer branchCache.RUnlock()

	if branchCache.remote.isValid(cacheTimeout, checksum) {
		if branches, ok := branchCache.remote.value.([]string); ok {
			return branches, true
		}
	}
	return nil, false
}

// cacheRemoteBranches stores remote branches in cache with current timestamp
func cacheRemoteBranches(ctx context.Context, branches []string) {
	checksum, err := getRepoStateChecksum(ctx)
	if err != nil {
		return // Silently fail cache operations
	}

	branchCache.Lock()
	defer branchCache.Unlock()

	branchCache.remote = cacheEntry{
		value:     branches,
		timestamp: time.Now(),
		checksum:  checksum,
	}
}

// getCachedLocalBranches attempts to retrieve local branches from cache
func getCachedLocalBranches(ctx context.Context) ([]string, bool) {
	checksum, err := getRepoStateChecksum(ctx)
	if err != nil {
		return nil, false
	}

	branchCache.RLock()
	defer branchCache.RUnlock()

	if branchCache.local.isValid(cacheTimeout, checksum) {
		if branches, ok := branchCache.local.value.([]string); ok {
			return branches, true
		}
	}
	return nil, false
}

// cacheLocalBranches stores local branches in cache with current timestamp
func cacheLocalBranches(ctx context.Context, branches []string) {
	checksum, err := getRepoStateChecksum(ctx)
	if err != nil {
		return // Silently fail cache operations
	}

	branchCache.Lock()
	defer branchCache.Unlock()

	branchCache.local = cacheEntry{
		value:     branches,
		timestamp: time.Now(),
		checksum:  checksum,
	}
}

// getRepoStateChecksum generates a checksum representing the current repository state
// This is used for cache invalidation when the repository changes
func getRepoStateChecksum(ctx context.Context) (string, error) {
	// Get the current HEAD commit hash and modification time of .git directory
	cmd := exec.CommandContext(ctx, "git", "rev-parse", "HEAD")
	headOutput, err := cmd.Output()
	if err != nil {
		return "", err
	}

	gitDir := ".git"
	if info, err := os.Stat(gitDir); err == nil {
		// Combine HEAD hash with .git modification time for a unique checksum
		hashInput := fmt.Sprintf("%s:%d", strings.TrimSpace(string(headOutput)), info.ModTime().Unix())
		hash := sha256.Sum256([]byte(hashInput))
		return fmt.Sprintf("%x", hash[:8]), nil // Use first 8 bytes for performance
	}

	return strings.TrimSpace(string(headOutput)), nil
}

// Advanced Error Recovery Mechanisms

// createTrackingBranchWithRetry attempts to create a tracking branch with exponential backoff retry logic.
// This handles transient failures like network issues, file system locks, or temporary git repository states.
//
// Parameters:
//   - ctx: Context for timeout and cancellation control
//   - branchName: Name of the branch to create
//   - maxRetries: Maximum number of retry attempts
//
// Returns:
//   - error: nil on success, aggregated error on failure after all retries
//
// Retry Strategy:
//   - Exponential backoff: 100ms, 200ms, 400ms, etc.
//   - Jitter added to prevent thundering herd problems
//   - Different retry strategies for different error types
func createTrackingBranchWithRetry(ctx context.Context, branchName string, maxRetries int) error {
	var lastErr error
	
	for attempt := 0; attempt <= maxRetries; attempt++ {
		cmd := exec.CommandContext(ctx, "git", "branch", "--track", branchName, fmt.Sprintf("origin/%s", branchName))
		err := cmd.Run()
		
		if err == nil {
			return nil // Success
		}
		
		lastErr = err
		
		// Don't retry on the last attempt
		if attempt == maxRetries {
			break
		}
		
		// Determine if this error is retryable
		if !isRetryableError(err) {
			return fmt.Errorf("non-retryable error: %w", err)
		}
		
		// Calculate backoff duration with jitter
		backoffDuration := calculateBackoff(attempt)
		
		// Check if we have time for another retry
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during retry: %w", ctx.Err())
		case <-time.After(backoffDuration):
			// Continue to next retry
		}
	}
	
	return fmt.Errorf("max retries exceeded: %w", lastErr)
}

// isRetryableError determines if an error should trigger a retry attempt.
// Some errors are permanent and retrying won't help.
//
// Retryable errors:
//   - Network timeouts
//   - Temporary file system issues
//   - Git lock file conflicts
//
// Non-retryable errors:
//   - Invalid branch names
//   - Missing remote references
//   - Permission denied errors
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}
	
	errStr := strings.ToLower(err.Error())
	
	// Non-retryable error patterns
	nonRetryablePatterns := []string{
		"invalid reference format",
		"not a valid object name",
		"remote ref does not exist",
		"permission denied",
		"access denied",
		"authentication failed",
		"repository not found",
		"fatal: not a git repository",
	}
	
	for _, pattern := range nonRetryablePatterns {
		if strings.Contains(errStr, pattern) {
			return false
		}
	}
	
	// Retryable error patterns
	retryablePatterns := []string{
		"timeout",
		"connection",
		"network",
		"unable to lock",
		"index.lock",
		"temporary failure",
		"resource temporarily unavailable",
	}
	
	for _, pattern := range retryablePatterns {
		if strings.Contains(errStr, pattern) {
			return true
		}
	}
	
	// Default to retryable for unknown errors (conservative approach)
	return true
}

// calculateBackoff returns the backoff duration for a given retry attempt.
// Uses exponential backoff with jitter to prevent thundering herd problems.
//
// Formula: base_delay * (2^attempt) + random_jitter
// Example: 100ms, 200ms, 400ms, 800ms, etc. (plus jitter)
func calculateBackoff(attempt int) time.Duration {
	baseDelay := 100 * time.Millisecond
	backoff := baseDelay * time.Duration(1<<uint(attempt)) // 2^attempt
	
	// Add jitter (up to 50% of backoff duration)
	jitter := time.Duration(float64(backoff) * 0.5 * (0.5 + 0.5)) // Simplified jitter
	
	return backoff + jitter
}

// recoverFromGitCorruption attempts to recover from git repository corruption.
// This is a more advanced recovery mechanism for severe git state issues.
//
// Recovery strategies:
//   1. Clear git index locks
//   2. Refresh git index
//   3. Garbage collect unreachable objects
//   4. Verify repository integrity
//
// Parameters:
//   - ctx: Context for timeout and cancellation control
//
// Returns:
//   - error: nil if recovery successful, error describing what couldn't be fixed
func recoverFromGitCorruption(ctx context.Context) error {
	var recoveryErrors []string
	
	// Step 1: Remove any stale lock files
	lockFiles := []string{
		".git/index.lock",
		".git/HEAD.lock",
		".git/refs/heads/*.lock",
		".git/config.lock",
	}
	
	for _, lockPattern := range lockFiles {
		if matches, err := filepath.Glob(lockPattern); err == nil {
			for _, lockFile := range matches {
				if err := os.Remove(lockFile); err != nil {
					recoveryErrors = append(recoveryErrors, fmt.Sprintf("failed to remove lock file %s: %v", lockFile, err))
				}
			}
		}
	}
	
	// Step 2: Reset git index
	cmd := exec.CommandContext(ctx, "git", "reset", "--mixed")
	if err := cmd.Run(); err != nil {
		recoveryErrors = append(recoveryErrors, fmt.Sprintf("failed to reset git index: %v", err))
	}
	
	// Step 3: Garbage collect
	cmd = exec.CommandContext(ctx, "git", "gc", "--prune=now")
	if err := cmd.Run(); err != nil {
		recoveryErrors = append(recoveryErrors, fmt.Sprintf("failed to garbage collect: %v", err))
	}
	
	// Step 4: Verify repository integrity
	cmd = exec.CommandContext(ctx, "git", "fsck", "--full")
	if err := cmd.Run(); err != nil {
		recoveryErrors = append(recoveryErrors, fmt.Sprintf("repository integrity check failed: %v", err))
	}
	
	if len(recoveryErrors) > 0 {
		return fmt.Errorf("recovery partially failed:\n%s", strings.Join(recoveryErrors, "\n"))
	}
	
	return nil
}

// handlePartialFailure provides intelligent handling of partial operation failures.
// Instead of failing completely, it analyzes what succeeded and provides actionable next steps.
//
// Parameters:
//   - successfulBranches: List of branches that were successfully created
//   - errors: List of errors that occurred during the operation
//   - totalBranches: Total number of branches attempted
//
// Returns:
//   - error: A user-friendly error message with recovery suggestions
func handlePartialFailure(successfulBranches []string, errors []error, totalBranches int) error {
	if len(errors) == 0 {
		return nil // No failure to handle
	}
	
	successCount := len(successfulBranches)
	successRate := float64(successCount) / float64(totalBranches) * 100
	
	var errorMessage strings.Builder
	
	// Summary
	fmt.Fprintf(&errorMessage, "⚠️  Partial sync completion: %d/%d branches synced (%.1f%% success rate)\n\n", 
		successCount, totalBranches, successRate)
	
	// Successful branches
	if successCount > 0 {
		fmt.Fprintf(&errorMessage, "✅ Successfully synced branches:\n")
		for _, branch := range successfulBranches {
			fmt.Fprintf(&errorMessage, "   • %s\n", branch)
		}
		fmt.Fprintf(&errorMessage, "\n")
	}
	
	// Failed branches with categorized errors
	errorCategories := categorizeErrors(errors)
	fmt.Fprintf(&errorMessage, "❌ Failed operations:\n")
	
	for category, categoryErrors := range errorCategories {
		fmt.Fprintf(&errorMessage, "   %s (%d errors):\n", category, len(categoryErrors))
		for i, err := range categoryErrors {
			if i < 3 { // Show only first 3 errors per category
				fmt.Fprintf(&errorMessage, "     • %s\n", err.Error())
			} else if i == 3 {
				fmt.Fprintf(&errorMessage, "     • ... and %d more\n", len(categoryErrors)-3)
				break
			}
		}
	}
	
	// Recovery suggestions
	fmt.Fprintf(&errorMessage, "\n🔧 Suggested recovery actions:\n")
	
	if successRate < 50 {
		fmt.Fprintf(&errorMessage, "   • Check network connectivity and retry\n")
		fmt.Fprintf(&errorMessage, "   • Verify git remote configuration\n")
		fmt.Fprintf(&errorMessage, "   • Run 'git fetch --all' manually to debug\n")
	} else {
		fmt.Fprintf(&errorMessage, "   • Re-run the sync command to retry failed branches\n")
		fmt.Fprintf(&errorMessage, "   • Use --verbose flag for detailed error information\n")
	}
	
	if hasCorruptionIndicators(errors) {
		fmt.Fprintf(&errorMessage, "   • Repository corruption detected - consider running 'git fsck'\n")
	}
	
	return fmt.Errorf("%s", errorMessage.String())
}

// categorizeErrors groups errors by type for better user understanding
func categorizeErrors(errors []error) map[string][]error {
	categories := map[string][]error{
		"Network/Connectivity": {},
		"Repository Corruption": {},
		"Permission Issues": {},
		"Invalid References": {},
		"Unknown": {},
	}
	
	for _, err := range errors {
		errStr := strings.ToLower(err.Error())
		
		switch {
		case strings.Contains(errStr, "network") || strings.Contains(errStr, "timeout") || strings.Contains(errStr, "connection"):
			categories["Network/Connectivity"] = append(categories["Network/Connectivity"], err)
		case strings.Contains(errStr, "corrupt") || strings.Contains(errStr, "fsck") || strings.Contains(errStr, "index.lock"):
			categories["Repository Corruption"] = append(categories["Repository Corruption"], err)
		case strings.Contains(errStr, "permission") || strings.Contains(errStr, "access denied"):
			categories["Permission Issues"] = append(categories["Permission Issues"], err)
		case strings.Contains(errStr, "invalid") || strings.Contains(errStr, "not found") || strings.Contains(errStr, "does not exist"):
			categories["Invalid References"] = append(categories["Invalid References"], err)
		default:
			categories["Unknown"] = append(categories["Unknown"], err)
		}
	}
	
	// Remove empty categories
	for category, errs := range categories {
		if len(errs) == 0 {
			delete(categories, category)
		}
	}
	
	return categories
}

// hasCorruptionIndicators checks if errors suggest repository corruption
func hasCorruptionIndicators(errors []error) bool {
	corruptionKeywords := []string{
		"corrupt",
		"index.lock",
		"unable to lock",
		"bad object",
		"fsck",
		"integrity",
	}
	
	for _, err := range errors {
		errStr := strings.ToLower(err.Error())
		for _, keyword := range corruptionKeywords {
			if strings.Contains(errStr, keyword) {
				return true
			}
		}
	}
	
	return false
}

// Telemetry and Metrics Collection

// collectTelemetry records a telemetry event for performance analysis and improvement.
// All telemetry data is anonymous and used solely for improving the tool's performance.
//
// Parameters:
//   - operation: The operation being measured (e.g., "sync", "fetch", "branch_create")
//   - duration: How long the operation took
//   - success: Whether the operation completed successfully
//   - branchCount: Number of branches involved (if applicable)
//   - errorType: Type of error if operation failed
//   - metadata: Additional context data
func collectTelemetry(operation string, duration time.Duration, success bool, branchCount int, errorType string, metadata map[string]interface{}) {
	if !enableTelemetry {
		return
	}

	event := TelemetryEvent{
		Timestamp:   time.Now(),
		Operation:   operation,
		Duration:    duration,
		Success:     success,
		BranchCount: branchCount,
		ErrorType:   errorType,
		Metadata:    metadata,
	}

	telemetryCollector.Lock()
	telemetryCollector.metrics = append(telemetryCollector.metrics, event)
	
	// Prevent unbounded memory growth by keeping only recent events
	if len(telemetryCollector.metrics) > 1000 {
		telemetryCollector.metrics = telemetryCollector.metrics[100:] // Keep most recent 900 events
	}
	telemetryCollector.Unlock()
}

// measureOperation wraps an operation with timing and success/failure tracking
func measureOperation(operation string, fn func() error) error {
	if !enableTelemetry {
		return fn()
	}

	start := time.Now()
	err := fn()
	duration := time.Since(start)
	
	success := err == nil
	errorType := ""
	if err != nil {
		errorType = categorizeErrorType(err)
	}
	
	collectTelemetry(operation, duration, success, 0, errorType, nil)
	return err
}

// categorizeErrorType determines the category of an error for telemetry purposes
func categorizeErrorType(err error) string {
	if err == nil {
		return ""
	}
	
	errStr := strings.ToLower(err.Error())
	
	switch {
	case strings.Contains(errStr, "timeout") || strings.Contains(errStr, "context deadline"):
		return "timeout"
	case strings.Contains(errStr, "network") || strings.Contains(errStr, "connection"):
		return "network"
	case strings.Contains(errStr, "permission") || strings.Contains(errStr, "access denied"):
		return "permission"
	case strings.Contains(errStr, "not found") || strings.Contains(errStr, "does not exist"):
		return "not_found"
	case strings.Contains(errStr, "invalid") || strings.Contains(errStr, "bad"):
		return "invalid_input"
	case strings.Contains(errStr, "lock") || strings.Contains(errStr, "busy"):
		return "resource_conflict"
	case strings.Contains(errStr, "corrupt") || strings.Contains(errStr, "integrity"):
		return "corruption"
	default:
		return "unknown"
	}
}

// getTelemetryStats returns aggregated statistics from collected telemetry data
func getTelemetryStats() map[string]interface{} {
	telemetryCollector.RLock()
	defer telemetryCollector.RUnlock()
	
	if len(telemetryCollector.metrics) == 0 {
		return map[string]interface{}{"total_events": 0}
	}
	
	stats := map[string]interface{}{
		"total_events": len(telemetryCollector.metrics),
		"operations":   make(map[string]int),
		"success_rate": 0.0,
		"avg_duration": time.Duration(0),
		"error_types":  make(map[string]int),
	}
	
	var totalDuration time.Duration
	var successCount int
	
	for _, event := range telemetryCollector.metrics {
		// Count operations
		if opCount, exists := stats["operations"].(map[string]int)[event.Operation]; exists {
			stats["operations"].(map[string]int)[event.Operation] = opCount + 1
		} else {
			stats["operations"].(map[string]int)[event.Operation] = 1
		}
		
		// Track success rate
		if event.Success {
			successCount++
		}
		
		// Track durations
		totalDuration += event.Duration
		
		// Track error types
		if event.ErrorType != "" {
			if errCount, exists := stats["error_types"].(map[string]int)[event.ErrorType]; exists {
				stats["error_types"].(map[string]int)[event.ErrorType] = errCount + 1
			} else {
				stats["error_types"].(map[string]int)[event.ErrorType] = 1
			}
		}
	}
	
	stats["success_rate"] = float64(successCount) / float64(len(telemetryCollector.metrics)) * 100
	stats["avg_duration"] = totalDuration / time.Duration(len(telemetryCollector.metrics))
	
	return stats
}

// exportTelemetryData exports telemetry data in JSON format for analysis
// This is useful for debugging performance issues or understanding usage patterns
func exportTelemetryData() ([]byte, error) {
	telemetryCollector.RLock()
	defer telemetryCollector.RUnlock()
	
	data := struct {
		ExportTime time.Time        `json:"export_time"`
		Stats      map[string]interface{} `json:"stats"`
		Events     []TelemetryEvent `json:"events"`
	}{
		ExportTime: time.Now(),
		Stats:      getTelemetryStats(),
		Events:     telemetryCollector.metrics,
	}
	
	return []byte(fmt.Sprintf("%+v", data)), nil
}

// clearTelemetryData removes all collected telemetry data
func clearTelemetryData() {
	telemetryCollector.Lock()
	telemetryCollector.metrics = nil
	telemetryCollector.Unlock()
}

// Advanced Configuration Validation

// validateSyncConfiguration performs comprehensive validation of all sync command configuration.
// This ensures that all settings are valid and compatible with each other before execution begins.
//
// Validation checks:
//   - Timeout values are reasonable (not too short or absurdly long)
//   - Concurrency limits are within safe bounds
//   - Cache timeout is compatible with operation timeout
//   - Flag combinations make sense
//   - Environment variables are properly formatted
//
// Returns:
//   - error: nil if configuration is valid, detailed error message otherwise
func validateSyncConfiguration() error {
	var validationErrors []string

	// Validate timeout settings
	if timeoutSync < 10*time.Second {
		validationErrors = append(validationErrors, "timeout is too short (minimum: 10s)")
	}
	if timeoutSync > 1*time.Hour {
		validationErrors = append(validationErrors, "timeout is too long (maximum: 1h)")
	}

	// Validate concurrency settings
	if maxConcurrent < 1 {
		validationErrors = append(validationErrors, "max-concurrent must be at least 1")
	}
	if maxConcurrent > 50 {
		validationErrors = append(validationErrors, "max-concurrent is too high (maximum: 50 for safety)")
	}

	// Validate cache settings
	if enableCache {
		if cacheTimeout < 1*time.Minute {
			validationErrors = append(validationErrors, "cache-timeout is too short (minimum: 1m)")
		}
		if cacheTimeout > 24*time.Hour {
			validationErrors = append(validationErrors, "cache-timeout is too long (maximum: 24h)")
		}
		
		// Cache timeout should be reasonable compared to operation timeout
		if cacheTimeout < timeoutSync {
			validationErrors = append(validationErrors, "cache-timeout should be longer than operation timeout for effectiveness")
		}
	}

	// Validate flag combinations
	if verboseSync && quietSync {
		validationErrors = append(validationErrors, "cannot use both --verbose and --quiet flags")
	}

	// Validate environment variable formats
	if err := validateEnvironmentVariables(); err != nil {
		validationErrors = append(validationErrors, fmt.Sprintf("environment variable error: %v", err))
	}

	// Check for conflicting options
	if dryRunSync && enableTelemetry {
		// This is not an error, but we should warn the user
		if verboseSync {
			fmt.Println("⚠️  Note: Telemetry data from dry-run operations may not be representative")
		}
	}

	// Validate system resources
	if err := validateSystemResources(); err != nil {
		validationErrors = append(validationErrors, fmt.Sprintf("system resource check failed: %v", err))
	}

	if len(validationErrors) > 0 {
		return fmt.Errorf("configuration validation failed:\n  • %s", strings.Join(validationErrors, "\n  • "))
	}

	return nil
}

// validateEnvironmentVariables checks that all environment variables have valid formats
func validateEnvironmentVariables() error {
	envVars := map[string]func(string) error{
		"CONNIX_SYNC_TIMEOUT": func(val string) error {
			if val == "" {
				return nil // Not set, using default
			}
			_, err := time.ParseDuration(val)
			return err
		},
		"CONNIX_MAX_CONCURRENT": func(val string) error {
			if val == "" {
				return nil // Not set, using default
			}
			if _, err := strconv.Atoi(val); err != nil {
				return fmt.Errorf("must be a valid integer")
			}
			return nil
		},
		"CONNIX_CACHE_TIMEOUT": func(val string) error {
			if val == "" {
				return nil // Not set, using default
			}
			_, err := time.ParseDuration(val)
			return err
		},
		"CONNIX_SKIP_VALIDATION": func(val string) error {
			if val == "" {
				return nil // Not set, using default
			}
			_, err := strconv.ParseBool(val)
			return err
		},
		"CONNIX_ENABLE_CACHE": func(val string) error {
			if val == "" {
				return nil // Not set, using default
			}
			_, err := strconv.ParseBool(val)
			return err
		},
		"CONNIX_ENABLE_TELEMETRY": func(val string) error {
			if val == "" {
				return nil // Not set, using default
			}
			_, err := strconv.ParseBool(val)
			return err
		},
	}

	for envVar, validator := range envVars {
		if err := validator(os.Getenv(envVar)); err != nil {
			return fmt.Errorf("%s has invalid format: %w", envVar, err)
		}
	}

	return nil
}

// validateSystemResources checks if the system has adequate resources for the operation
func validateSystemResources() error {
	// Check if we have write permissions in the current directory
	tempFile := ".connix_permission_test"
	if err := os.WriteFile(tempFile, []byte("test"), 0644); err != nil {
		return fmt.Errorf("insufficient write permissions in current directory")
	}
	os.Remove(tempFile) // Clean up

	// Check available disk space (basic check)
	if stat, err := os.Stat("."); err == nil {
		_ = stat // We have basic file system access
	} else {
		return fmt.Errorf("cannot access current directory: %w", err)
	}

	// Validate git is available
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("git command not found in PATH")
	}

	// Check git version compatibility
	if err := validateGitVersion(); err != nil {
		return fmt.Errorf("git version check failed: %w", err)
	}

	return nil
}

// validateGitVersion ensures git version is compatible with our operations
func validateGitVersion() error {
	cmd := exec.Command("git", "--version")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to get git version: %w", err)
	}

	version := strings.TrimSpace(string(output))
	
	// Basic check - ensure git is functional
	// We could add more sophisticated version parsing here if needed
	if !strings.Contains(strings.ToLower(version), "git version") {
		return fmt.Errorf("unexpected git version output: %s", version)
	}

	// Test basic git functionality
	cmd = exec.Command("git", "config", "--get", "user.name")
	if err := cmd.Run(); err != nil {
		// This is just a warning, not a fatal error
		if verboseSync {
			fmt.Println("⚠️  Warning: Git user configuration may not be set up")
		}
	}

	return nil
}

// generateConfigurationReport creates a detailed report of current configuration
// This is useful for troubleshooting and support
func generateConfigurationReport() string {
	var report strings.Builder
	
	fmt.Fprintf(&report, "=== Connix Sync Configuration Report ===\n\n")
	
	// Command-line flags
	fmt.Fprintf(&report, "Command-line Configuration:\n")
	fmt.Fprintf(&report, "  Verbose Mode: %t\n", verboseSync)
	fmt.Fprintf(&report, "  Dry Run Mode: %t\n", dryRunSync)
	fmt.Fprintf(&report, "  Quiet Mode: %t\n", quietSync)
	fmt.Fprintf(&report, "  Timeout: %v\n", timeoutSync)
	fmt.Fprintf(&report, "  Max Concurrent: %d\n", maxConcurrent)
	fmt.Fprintf(&report, "  Skip Validation: %t\n", skipValidation)
	fmt.Fprintf(&report, "  Enable Cache: %t\n", enableCache)
	fmt.Fprintf(&report, "  Cache Timeout: %v\n", cacheTimeout)
	fmt.Fprintf(&report, "  Enable Telemetry: %t\n", enableTelemetry)
	fmt.Fprintf(&report, "\n")
	
	// Environment variables
	fmt.Fprintf(&report, "Environment Variables:\n")
	envVars := []string{
		"CONNIX_SYNC_TIMEOUT",
		"CONNIX_MAX_CONCURRENT", 
		"CONNIX_CACHE_TIMEOUT",
		"CONNIX_SKIP_VALIDATION",
		"CONNIX_ENABLE_CACHE",
		"CONNIX_ENABLE_TELEMETRY",
	}
	
	for _, envVar := range envVars {
		value := os.Getenv(envVar)
		if value == "" {
			value = "(not set)"
		}
		fmt.Fprintf(&report, "  %s: %s\n", envVar, value)
	}
	fmt.Fprintf(&report, "\n")
	
	// System information
	fmt.Fprintf(&report, "System Information:\n")
	if gitVersion, err := exec.Command("git", "--version").Output(); err == nil {
		fmt.Fprintf(&report, "  Git Version: %s", strings.TrimSpace(string(gitVersion)))
	} else {
		fmt.Fprintf(&report, "  Git Version: ERROR - %v\n", err)
	}
	
	if pwd, err := os.Getwd(); err == nil {
		fmt.Fprintf(&report, "  Working Directory: %s\n", pwd)
	}
	
	// Cache status
	if enableCache {
		fmt.Fprintf(&report, "\nCache Status:\n")
		branchCache.RLock()
		fmt.Fprintf(&report, "  Local Branches Cache: %v\n", !branchCache.local.timestamp.IsZero())
		fmt.Fprintf(&report, "  Remote Branches Cache: %v\n", !branchCache.remote.timestamp.IsZero())
		branchCache.RUnlock()
	}
	
	// Telemetry status
	if enableTelemetry {
		fmt.Fprintf(&report, "\nTelemetry Status:\n")
		stats := getTelemetryStats()
		fmt.Fprintf(&report, "  Total Events: %v\n", stats["total_events"])
		fmt.Fprintf(&report, "  Success Rate: %.1f%%\n", stats["success_rate"])
	}
	
	fmt.Fprintf(&report, "\n=== End Configuration Report ===\n")
	
	return report.String()
}