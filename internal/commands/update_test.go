package commands

import (
	"bytes"
	"testing"

	"github.com/sleuth-io/sx/v2/internal/buildinfo"
)

func TestUpdateCommandDevBuild(t *testing.T) {
	// Save original version
	originalVersion := buildinfo.Version
	defer func() { buildinfo.Version = originalVersion }()

	// Set version to "dev"
	buildinfo.Version = "dev"

	cmd := NewUpdateCommand()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	// Run with --check flag
	cmd.SetArgs([]string{"--check"})

	err := cmd.Execute()
	if err != nil {
		t.Fatalf("Expected no error for dev build, got: %v", err)
	}

	output := buf.String()
	if output != "Cannot update development builds. Please install from a release.\n" {
		t.Errorf("Unexpected output: %s", output)
	}
}

func TestUpdateCommandFlags(t *testing.T) {
	cmd := NewUpdateCommand()

	// Check that --check flag exists
	checkFlag := cmd.Flags().Lookup("check")
	if checkFlag == nil {
		t.Error("Expected --check flag to exist")
		return
	}

	if checkFlag.DefValue != "false" {
		t.Errorf("Expected --check default to be false, got %s", checkFlag.DefValue)
	}
}

func TestUpdateCommandMetadata(t *testing.T) {
	cmd := NewUpdateCommand()

	if cmd.Use != "update" {
		t.Errorf("Expected Use to be 'update', got '%s'", cmd.Use)
	}

	if cmd.Short == "" {
		t.Error("Expected Short description to be set")
	}

	if cmd.Long == "" {
		t.Error("Expected Long description to be set")
	}
}

// TestUpdateCommandHelp verifies help output works
func TestUpdateCommandHelp(t *testing.T) {
	cmd := NewUpdateCommand()
	buf := new(bytes.Buffer)
	cmd.SetOut(buf)

	cmd.SetArgs([]string{"--help"})

	// Help returns an error (which is normal behavior for cobra)
	_ = cmd.Execute()

	output := buf.String()
	// Just verify that something was written
	if len(output) < 10 {
		t.Errorf("Expected substantial help output, got: %s", output)
	}
}
