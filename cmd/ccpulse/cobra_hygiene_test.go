package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestStatusRejectsExtraArgs(t *testing.T) {
	root := newRootCmd()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"status", "foo", "bar"})

	err := root.Execute()
	if err == nil {
		t.Fatalf("expected error from `status foo bar`, got nil")
	}
	combined := out.String() + errOut.String()
	if strings.Contains(combined, "Usage:") {
		t.Errorf("unexpected usage wall in output:\n%s", combined)
	}
}

func TestRootDoesNotDoublePrintErrors(t *testing.T) {
	root := newRootCmd()
	sentinel := errors.New("boom-7f3a")
	root.AddCommand(&cobra.Command{
		Use:  "boomtest",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return sentinel
		},
	})

	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"boomtest"})

	err := root.Execute()
	if err == nil {
		t.Fatalf("expected error from boomtest, got nil")
	}

	combined := out.String() + errOut.String()
	if n := strings.Count(combined, sentinel.Error()); n > 0 {
		t.Errorf("cobra printed the error %d times; want 0 (main is the sole printer)\noutput:\n%s", n, combined)
	}
	if strings.Contains(combined, "Usage:") {
		t.Errorf("unexpected usage wall in output:\n%s", combined)
	}
}

func TestLeafCommandsRejectExtraArgs(t *testing.T) {
	for _, cmd := range []string{"version", "doctor", "index", "status"} {
		t.Run(cmd, func(t *testing.T) {
			root := newRootCmd()
			var out, errOut bytes.Buffer
			root.SetOut(&out)
			root.SetErr(&errOut)
			root.SetArgs([]string{cmd, "extra"})
			if err := root.Execute(); err == nil {
				t.Fatalf("%s extra: expected error, got nil", cmd)
			}
		})
	}
}
