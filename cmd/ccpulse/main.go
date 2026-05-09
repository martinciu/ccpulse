package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const version = "v0.0.0"

func versionString() string {
	return "ccpulse " + version
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "ccpulse",
		Short: "Claude Code usage TUI dashboard",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(cmd.OutOrStdout())
		},
	}
	root.AddCommand(newStatusCmd())
	root.AddCommand(newIndexCmd())
	root.AddCommand(newConfigCmd())
	root.AddCommand(newDoctorCmd())
	root.AddCommand(newVersionCmd())
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), versionString())
		},
	}
}

func newConfigCmd() *cobra.Command {
	c := &cobra.Command{Use: "config"}
	c.AddCommand(&cobra.Command{Use: "edit"}, &cobra.Command{Use: "show"}, &cobra.Command{Use: "path"})
	return c
}
func newDoctorCmd() *cobra.Command {
	return &cobra.Command{Use: "doctor", RunE: func(cmd *cobra.Command, args []string) error { return nil }}
}

// Stub — filled in by Task 28 (TUI launch).
func runTUI(out interface{}) error { return nil }

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
