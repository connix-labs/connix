/*
Copyright © 2025 connix-labs

*/

package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFindGitWorktreeRoot(t *testing.T) {
	tests := []struct {
		name        string
		setupFunc   func(t *testing.T) string
		expectError bool
	}{
		{
			name: "regular git repository",
			setupFunc: func(t *testing.T) string {
				tmpDir := t.TempDir()
				gitDir := filepath.Join(tmpDir, ".git")
				if err := os.Mkdir(gitDir, 0755); err != nil {
					t.Fatalf("failed to create .git directory: %v", err)
				}
				if err := os.Chdir(tmpDir); err != nil {
					t.Fatalf("failed to change to tmp directory: %v", err)
				}
				return tmpDir
			},
			expectError: false,
		},
		{
			name: "git worktree with gitdir file",
			setupFunc: func(t *testing.T) string {
				tmpDir := t.TempDir()
				gitFile := filepath.Join(tmpDir, ".git")
				if err := os.WriteFile(gitFile, []byte("gitdir: /some/path/.git/worktrees/branch"), 0644); err != nil {
					t.Fatalf("failed to create .git file: %v", err)
				}
				if err := os.Chdir(tmpDir); err != nil {
					t.Fatalf("failed to change to tmp directory: %v", err)
				}
				return tmpDir
			},
			expectError: false,
		},
		{
			name: "subdirectory of git repository",
			setupFunc: func(t *testing.T) string {
				tmpDir := t.TempDir()
				gitDir := filepath.Join(tmpDir, ".git")
				if err := os.Mkdir(gitDir, 0755); err != nil {
					t.Fatalf("failed to create .git directory: %v", err)
				}
				subDir := filepath.Join(tmpDir, "subdir")
				if err := os.Mkdir(subDir, 0755); err != nil {
					t.Fatalf("failed to create subdirectory: %v", err)
				}
				if err := os.Chdir(subDir); err != nil {
					t.Fatalf("failed to change to subdirectory: %v", err)
				}
				return tmpDir
			},
			expectError: false,
		},
		{
			name: "non-git directory",
			setupFunc: func(t *testing.T) string {
				tmpDir := t.TempDir()
				if err := os.Chdir(tmpDir); err != nil {
					t.Fatalf("failed to change to tmp directory: %v", err)
				}
				return tmpDir
			},
			expectError: true,
		},
	}

	originalCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get original working directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(originalCwd); err != nil {
			t.Errorf("failed to restore original working directory: %v", err)
		}
	}()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			expectedRoot := tt.setupFunc(t)

			root, err := findGitWorktreeRoot()

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error, but got none")
				}
				return
			}

			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}

			if root != expectedRoot {
				t.Errorf("expected root %s, got %s", expectedRoot, root)
			}
		})
	}
}

func TestGetLocalBranches(t *testing.T) {
	originalCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get original working directory: %v", err)
	}
	defer func() {
		dErr := os.Chdir(originalCwd)
		if dErr != nil {
			t.Errorf("failed to restore original working directory: %v", err)
		}
	}()

	tmpDir := t.TempDir()
	err = os.Chdir(tmpDir)
	if err != nil {
		t.Fatalf("failed to change to tmp directory: %v", err)
	}

	ctx := context.Background()
	branches, err := getLocalBranches(ctx)

	if err == nil {
		t.Errorf("expected error when not in git repository, but got branches: %v", branches)
	}
}

func TestGetRemoteBranches(t *testing.T) {
	originalCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get original working directory: %v", err)
	}
	defer func() {
		dErr := os.Chdir(originalCwd)
		if dErr != nil {
			t.Errorf("failed to restore original working directory: %v", err)
		}
	}()

	tmpDir := t.TempDir()
	err = os.Chdir(tmpDir)
	if err != nil {
		t.Fatalf("failed to change to tmp directory: %v", err)
	}

	ctx := context.Background()
	branches, err := getRemoteBranches(ctx)

	if err == nil {
		t.Errorf("expected error when not in git repository, but got branches: %v", branches)
	}
}

func TestIsGitRepository(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T) string
		expected bool
	}{
		{
			name: "not a git repository",
			setup: func(t *testing.T) string {
				return t.TempDir()
			},
			expected: false,
		},
		{
			name: "git repository with .git directory",
			setup: func(t *testing.T) string {
				tmpDir := t.TempDir()
				gitDir := filepath.Join(tmpDir, ".git")
				if err := os.Mkdir(gitDir, 0755); err != nil {
					t.Fatalf("failed to create .git directory: %v", err)
				}
				return tmpDir
			},
			expected: false,
		},
	}

	originalCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get original working directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(originalCwd); err != nil {
			t.Errorf("failed to restore original working directory: %v", err)
		}
	}()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			testDir := tt.setup(t)
			if err := os.Chdir(testDir); err != nil {
				t.Fatalf("failed to change to test directory: %v", err)
			}

			result := isGitRepository()
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestValidateGitWorktreeRootWithGitFile(t *testing.T) {
	tmpDir := t.TempDir()
	gitFile := filepath.Join(tmpDir, ".git")

	gitContent := "gitdir: /some/other/path/.git/worktrees/feature-branch"
	if err := os.WriteFile(gitFile, []byte(gitContent), 0644); err != nil {
		t.Fatalf("failed to create .git file: %v", err)
	}

	originalCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get original working directory: %v", err)
	}
	defer func() {
		dErr := os.Chdir(originalCwd)
		if dErr != nil {
			t.Errorf("failed to restore original working directory: %v", err)
		}
	}()

	err = os.Chdir(tmpDir)
	if err != nil {
		t.Fatalf("failed to change to tmp directory: %v", err)
	}

	root, err := findGitWorktreeRoot()
	if err != nil {
		t.Errorf("unexpected error finding worktree root: %v", err)
	}

	if root != tmpDir {
		t.Errorf("expected root %s, got %s", tmpDir, root)
	}
}

func TestBranchFiltering(t *testing.T) {
	remoteBranches := []string{"main", "develop", "feature/test", "HEAD"}
	localBranches := []string{"main", "develop"}

	branchSet := make(map[string]bool)
	for _, branch := range localBranches {
		branchSet[branch] = true
	}

	var branchesToCreate []string
	for _, remoteBranch := range remoteBranches {
		if remoteBranch == "HEAD" {
			continue
		}

		if !branchSet[remoteBranch] {
			branchesToCreate = append(branchesToCreate, remoteBranch)
		}
	}

	expected := []string{"feature/test"}
	if len(branchesToCreate) != len(expected) {
		t.Errorf("expected %d branches to create, got %d", len(expected), len(branchesToCreate))
	}

	for i, branch := range branchesToCreate {
		if branch != expected[i] {
			t.Errorf("expected branch %s, got %s", expected[i], branch)
		}
	}
}

func TestRemoteBranchParsing(t *testing.T) {
	gitOutput := `origin/main
origin/develop
origin/feature/test
origin/HEAD`

	var branches []string
	for line := range strings.SplitSeq(gitOutput, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && strings.HasPrefix(line, "origin/") {
			branch := strings.TrimPrefix(line, "origin/")
			if branch != "HEAD" {
				branches = append(branches, branch)
			}
		}
	}

	expected := []string{"main", "develop", "feature/test"}
	if len(branches) != len(expected) {
		t.Errorf("expected %d branches, got %d", len(expected), len(branches))
	}

	for i, branch := range branches {
		if branch != expected[i] {
			t.Errorf("expected branch %s, got %s", expected[i], branch)
		}
	}
}

func TestGetEnvDuration(t *testing.T) {
	tests := []struct {
		name         string
		envValue     string
		defaultValue time.Duration
		expected     time.Duration
	}{
		{
			name:         "valid duration",
			envValue:     "30s",
			defaultValue: 5 * time.Minute,
			expected:     30 * time.Second,
		},
		{
			name:         "invalid duration",
			envValue:     "invalid",
			defaultValue: 5 * time.Minute,
			expected:     5 * time.Minute,
		},
		{
			name:         "empty env",
			envValue:     "",
			defaultValue: 2 * time.Hour,
			expected:     2 * time.Hour,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "TEST_DURATION_" + tt.name
			if tt.envValue != "" {
				err := os.Setenv(key, tt.envValue)
				if err != nil {
					t.Fatalf("failed to set env var: %v", err)
				}
				defer func() {
					dErr := os.Unsetenv(key)
					if dErr != nil {
						t.Errorf("failed to unset env var: %v", err)
					}
				}()
			}

			result := getEnvDuration(key, tt.defaultValue)
			if result != tt.expected {
				t.Errorf("expected %v, got %v", tt.expected, result)
			}
		})
	}
}

func TestGetEnvInt(t *testing.T) {
	tests := []struct {
		name         string
		envValue     string
		defaultValue int
		expected     int
	}{
		{
			name:         "valid int",
			envValue:     "10",
			defaultValue: 5,
			expected:     10,
		},
		{
			name:         "invalid int",
			envValue:     "not-a-number",
			defaultValue: 5,
			expected:     5,
		},
		{
			name:         "empty env",
			envValue:     "",
			defaultValue: 42,
			expected:     42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "TEST_INT_" + tt.name
			if tt.envValue != "" {
				err := os.Setenv(key, tt.envValue)
				if err != nil {
					t.Fatalf("failed to set env var: %v", err)
				}
				defer func() {
					dErr := os.Unsetenv(key)
					if dErr != nil {
						t.Errorf("failed to unset env var: %v", err)
					}
				}()
			}

			result := getEnvInt(key, tt.defaultValue)
			if result != tt.expected {
				t.Errorf("expected %d, got %d", tt.expected, result)
			}
		})
	}
}

func TestGetEnvBool(t *testing.T) {
	tests := []struct {
		name         string
		envValue     string
		defaultValue bool
		expected     bool
	}{
		{
			name:         "true value",
			envValue:     "true",
			defaultValue: false,
			expected:     true,
		},
		{
			name:         "false value",
			envValue:     "false",
			defaultValue: true,
			expected:     false,
		},
		{
			name:         "invalid bool",
			envValue:     "maybe",
			defaultValue: true,
			expected:     true,
		},
		{
			name:         "empty env",
			envValue:     "",
			defaultValue: false,
			expected:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "TEST_BOOL_" + tt.name
			if tt.envValue != "" {
				err := os.Setenv(key, tt.envValue)
				if err != nil {
					t.Fatalf("failed to set env var: %v", err)
				}
				defer func() {
					dErr := os.Unsetenv(key)
					if dErr != nil {
						t.Errorf("failed to unset env var: %v", err)
					}
				}()
			}

			result := getEnvBool(key, tt.defaultValue)
			if result != tt.expected {
				t.Errorf("expected %t, got %t", tt.expected, result)
			}
		})
	}
}

func TestBranchFilteringEdgeCases(t *testing.T) {
	tests := []struct {
		name           string
		remoteBranches []string
		localBranches  []string
		expectedNew    []string
	}{
		{
			name:           "all branches exist locally",
			remoteBranches: []string{"main", "develop"},
			localBranches:  []string{"main", "develop", "feature"},
			expectedNew:    []string{},
		},
		{
			name:           "no local branches",
			remoteBranches: []string{"main", "develop", "feature"},
			localBranches:  []string{},
			expectedNew:    []string{"main", "develop", "feature"},
		},
		{
			name:           "mixed case",
			remoteBranches: []string{"main", "develop", "feature/test", "bugfix/issue-123"},
			localBranches:  []string{"main", "feature/test"},
			expectedNew:    []string{"develop", "bugfix/issue-123"},
		},
		{
			name:           "HEAD branch filtered",
			remoteBranches: []string{"main", "HEAD", "develop"},
			localBranches:  []string{"main"},
			expectedNew:    []string{"develop"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			branchSet := make(map[string]bool)
			for _, branch := range tt.localBranches {
				branchSet[branch] = true
			}

			var branchesToCreate []string
			for _, remoteBranch := range tt.remoteBranches {
				if remoteBranch == "HEAD" {
					continue
				}

				if !branchSet[remoteBranch] {
					branchesToCreate = append(branchesToCreate, remoteBranch)
				}
			}

			if len(branchesToCreate) != len(tt.expectedNew) {
				t.Errorf("expected %d branches to create, got %d", len(tt.expectedNew), len(branchesToCreate))
			}

			for i, branch := range branchesToCreate {
				if i < len(tt.expectedNew) && branch != tt.expectedNew[i] {
					t.Errorf("expected branch %s, got %s", tt.expectedNew[i], branch)
				}
			}
		})
	}
}
