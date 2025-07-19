/*
Copyright © 2025 connix-labs

*/

// Package cmd implements the command-line interface for Connix.
//
// Connix is a CLI tool for managing container platforms, MCP (Model Context Protocol) servers,
// and development workflows. This package contains all the command implementations using the
// Cobra CLI framework.
package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

// rootCmd represents the base command when called without any subcommands.
// It serves as the entry point for all Connix CLI operations including MCP server management,
// git branch synchronization, and Claude Code hooks administration.
var rootCmd = &cobra.Command{
	Use:   "connix",
	Short: "CLI for Connix - Container Platform, MCP, and Tooling powered by Nix",
	Long: `Connix is a comprehensive CLI tool for managing:

• Container orchestration and platform management
• MCP (Model Context Protocol) servers with multi-transport support
• Git branch synchronization and repository management  
• Claude Code hooks for AI workflow integration

Examples:
  connix mcp stdio                    # Start MCP server with stdio transport
  connix sync --verbose              # Sync all git branches with detailed output
  connix hooks pre_tool_use test     # Test the pre_tool_use hook
  
For more information, visit: https://github.com/connix-labs/connix`,
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main() and serves as the entry point for the CLI application.
// It handles the execution of commands and ensures proper error handling and exit codes.
func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		// Exit with non-zero status code to indicate failure
		os.Exit(1)
	}
}

// init initializes the root command and sets up global configuration.
// This function is called automatically when the package is imported and
// configures any persistent flags that should be available to all subcommands.
func init() {
	// Global flags can be added here that will be available to all subcommands
	// For example:
	// rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default is $HOME/.connix.yaml)")
	// rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "verbose output")

	// Local flags only apply to the root command when called directly
	rootCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
