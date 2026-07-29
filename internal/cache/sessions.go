package cache

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sleuth-io/sx/v2/internal/utils"
)

// SessionCache provides fast conversation/session ID tracking for clients
// that fire hooks on every prompt rather than once per session.
//
// File format: Line-based, space-separated `session_id timestamp`
// Example:
//
//	668320d2-2fd8-4888-b33c-2a466fec86e7 2025-12-12T10:30:00Z
//	490b90b7-a2ce-4c2c-bb76-cb77b125df2f 2025-12-11T15:45:00Z
type SessionCache struct {
	filePath string
}

// NewSessionCache creates a session cache for the given client ID
func NewSessionCache(clientID string) (*SessionCache, error) {
	cacheDir, err := GetCacheDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get cache dir: %w", err)
	}

	filePath := filepath.Join(cacheDir, clientID+"-sessions")
	return &SessionCache{filePath: filePath}, nil
}

// HasSession checks if a session ID has been seen before.
// This is optimized for fast checks (~1ms) by scanning the file line by line.
func (s *SessionCache) HasSession(sessionID string) bool {
	if sessionID == "" {
		return false
	}

	file, err := os.Open(s.filePath)
	if err != nil {
		// File doesn't exist = session not seen
		return false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, " ", 2)
		if len(parts) >= 1 && parts[0] == sessionID {
			return true
		}
	}

	return false
}

// RecordSession records a session ID with the current timestamp.
// Should be called optimistically before installation starts.
func (s *SessionCache) RecordSession(sessionID string) error {
	if sessionID == "" {
		return nil
	}

	// Ensure directory exists
	if err := utils.EnsureDir(filepath.Dir(s.filePath)); err != nil {
		return fmt.Errorf("failed to create cache directory: %w", err)
	}

	// Open file for appending (create if not exists)
	file, err := os.OpenFile(s.filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open session file: %w", err)
	}
	defer file.Close()

	// Write new entry
	entry := fmt.Sprintf("%s %s\n", sessionID, time.Now().UTC().Format(time.RFC3339))
	if _, err := file.WriteString(entry); err != nil {
		return fmt.Errorf("failed to write session entry: %w", err)
	}

	return nil
}

// CullOldEntries removes entries older than the specified max age.
// This keeps the session file from growing indefinitely.
func (s *SessionCache) CullOldEntries(maxAge time.Duration) error {
	file, err := os.Open(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Nothing to cull
		}
		return fmt.Errorf("failed to open session file: %w", err)
	}
	defer file.Close()

	cutoff := time.Now().Add(-maxAge)
	var keepLines []string

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, " ", 2)
		if len(parts) < 2 {
			continue // Malformed line, skip
		}

		timestamp, err := time.Parse(time.RFC3339, parts[1])
		if err != nil {
			continue // Can't parse timestamp, skip
		}

		if timestamp.After(cutoff) {
			keepLines = append(keepLines, line)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to scan session file: %w", err)
	}

	// Write filtered content back
	content := strings.Join(keepLines, "\n")
	if len(keepLines) > 0 {
		content += "\n"
	}

	if err := os.WriteFile(s.filePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("failed to write filtered sessions: %w", err)
	}

	return nil
}

// Clear removes all session entries.
func (s *SessionCache) Clear() error {
	if err := os.Remove(s.filePath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove session file: %w", err)
	}
	return nil
}

// FilePath returns the path to the session cache file (for testing/debugging).
func (s *SessionCache) FilePath() string {
	return s.filePath
}
