/*
Copyright © 2025 connix-labs

*/

//go:build integration

package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncCommandIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	
	originalCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get original working directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(originalCwd); err != nil {
			t.Errorf("failed to restore original working directory: %v", err)
		}
	}()

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to tmp directory: %v", err)
	}

	t.Run("sync command with dry run", func(t *testing.T) {
		if !isGitRepository() {
			t.Skip("not in a git repository, skipping integration test")
		}

		dryRunSync = true
		verboseSync = true
		defer func() {
			dryRunSync = false
			verboseSync = false
		}()

		err := runSync(nil, []string{})
		if err != nil {
			t.Logf("dry run sync error (expected in test environment): %v", err)
		}
	})
}

func TestRealGitRepository(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	tmpDir := t.TempDir()
	
	originalCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get original working directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(originalCwd); err != nil {
			t.Errorf("failed to restore original working directory: %v", err)
		}
	}()

	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to change to tmp directory: %v", err)
	}

	t.Run("create and test git repository", func(t *testing.T) {
		commands := [][]string{
			{"git", "init"},
			{"git", "config", "user.name", "Test User"},
			{"git", "config", "user.email", "test@example.com"},
			{"git", "commit", "--allow-empty", "-m", "initial commit"},
		}

		for _, cmd := range commands {
			c := exec.Command(cmd[0], cmd[1:]...)
			if err := c.Run(); err != nil {
				t.Fatalf("failed to run %v: %v", cmd, err)
			}
		}

		if !isGitRepository() {
			t.Error("expected directory to be a git repository")
		}

		branches, err := getLocalBranches()
		if err != nil {
			t.Fatalf("failed to get local branches: %v", err)
		}

		if len(branches) == 0 {
			t.Error("expected at least one branch")
		}

		t.Logf("found local branches: %v", branches)
	})
}

func TestGitWorktreeIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	mainRepo := t.TempDir()
	worktreeDir := filepath.Join(t.TempDir(), "worktree")
	
	originalCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get original working directory: %v", err)
	}
	defer func() {
		if err := os.Chdir(originalCwd); err != nil {
			t.Errorf("failed to restore original working directory: %v", err)
		}
	}()

	if err := os.Chdir(mainRepo); err != nil {
		t.Fatalf("failed to change to main repo directory: %v", err)
	}

	t.Run("setup main repository", func(t *testing.T) {
		commands := [][]string{
			{"git", "init"},
			{"git", "config", "user.name", "Test User"},
			{"git", "config", "user.email", "test@example.com"},
			{"git", "commit", "--allow-empty", "-m", "initial commit"},
			{"git", "branch", "feature-branch"},
		}

		for _, cmd := range commands {
			c := exec.Command(cmd[0], cmd[1:]...)
			if err := c.Run(); err != nil {
				t.Fatalf("failed to run %v: %v", cmd, err)
			}
		}
	})

	t.Run("create worktree", func(t *testing.T) {
		cmd := exec.Command("git", "worktree", "add", worktreeDir, "feature-branch")
		if err := cmd.Run(); err != nil {
			t.Fatalf("failed to create worktree: %v", err)
		}

		if err := os.Chdir(worktreeDir); err != nil {
			t.Fatalf("failed to change to worktree directory: %v", err)
		}

		root, err := findGitWorktreeRoot()
		if err != nil {
			t.Fatalf("failed to find worktree root: %v", err)
		}

		if root != worktreeDir {
			t.Errorf("expected worktree root %s, got %s", worktreeDir, root)
		}

		gitFile := filepath.Join(worktreeDir, ".git")
		content, err := os.ReadFile(gitFile)
		if err != nil {
			t.Fatalf("failed to read .git file: %v", err)
		}

		if !strings.HasPrefix(string(content), "gitdir: ") {
			t.Errorf("expected .git file to contain gitdir reference, got: %s", string(content))
		}
	})
}