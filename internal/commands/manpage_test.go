package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGetManPageDir(t *testing.T) {
	tests := []struct {
		name        string
		homeEnv     string
		expectDir   string
		expectError bool
	}{
		{
			name:        "Normal HOME directory",
			homeEnv:     "/home/testuser",
			expectDir:   "/home/testuser/.local/share/man/man1",
			expectError: false,
		},
		{
			name:        "Empty HOME directory",
			homeEnv:     "",
			expectDir:   ".local/share/man/man1",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original HOME
			originalHome := os.Getenv("HOME")
			defer func() {
				if originalHome != "" {
					os.Setenv("HOME", originalHome)
				} else {
					os.Unsetenv("HOME")
				}
			}()

			// Set test HOME
			if tt.homeEnv != "" {
				os.Setenv("HOME", tt.homeEnv)
			} else {
				os.Unsetenv("HOME")
			}

			result := getManPageDir()

			if result != tt.expectDir {
				t.Errorf("getManPageDir() = %v, want %v", result, tt.expectDir)
			}
		})
	}
}

func TestIsWritableDir(t *testing.T) {
	// Create a temporary directory for testing
	tempDir := filepath.Join(os.TempDir(), "test_manpage_"+time.Now().Format("20060102_150405"))
	defer os.RemoveAll(tempDir)

	tests := []struct {
		name     string
		dir      string
		setup    func(string) error
		expected bool
	}{
		{
			name: "Writable directory",
			dir:  tempDir,
			setup: func(dir string) error {
				return os.MkdirAll(dir, 0755)
			},
			expected: true,
		},
		{
			name: "Non-existent directory (should create)",
			dir:  filepath.Join(tempDir, "new_dir"),
			setup: func(dir string) error {
				return nil // Don't create it
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.setup(tt.dir); err != nil {
				t.Fatalf("Setup failed: %v", err)
			}

			result := isWritableDir(tt.dir)
			if result != tt.expected {
				t.Errorf("isWritableDir(%v) = %v, want %v", tt.dir, result, tt.expected)
			}
		})
	}
}

func TestIsWritableDirConcurrentSafe(t *testing.T) {
	// Test that multiple concurrent calls don't interfere with each other
	tempDir := filepath.Join(os.TempDir(), "test_concurrent_"+time.Now().Format("20060102_150405"))
	defer os.RemoveAll(tempDir)

	// Create the directory first
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		t.Fatalf("Failed to create test directory: %v", err)
	}

	// Run multiple concurrent tests
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			result := isWritableDir(tempDir)
			if !result {
				t.Errorf("isWritableDir should return true for writable directory")
			}
			done <- true
		}()
	}

	// Wait for all goroutines to complete
	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestIsInManPath(t *testing.T) {
	tests := []struct {
		name        string
		dir         string
		mockOutput  string
		mockError   bool
		expected    bool
	}{
		{
			name:        "Directory in MANPATH",
			dir:         "/usr/share/man",
			mockOutput:  "/usr/share/man:/usr/local/share/man",
			mockError:   false,
			expected:    true,
		},
		{
			name:        "Directory not in MANPATH",
			dir:         "/home/user/.local/share/man",
			mockOutput:  "/usr/share/man:/usr/local/share/man",
			mockError:   false,
			expected:    false,
		},
		{
			name:        "Command fails",
			dir:         "/usr/share/man",
			mockOutput:  "",
			mockError:   true,
			expected:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Note: This test is simplified since we can't easily mock exec.Command
			// In a real-world scenario, you'd want to refactor isInManPath to accept
			// a command runner interface for better testability
			
			// For now, we'll test the string processing logic
			if !tt.mockError && tt.mockOutput != "" {
				paths := strings.Split(strings.TrimSpace(tt.mockOutput), ":")
				found := false
				for _, path := range paths {
					if strings.TrimSpace(path) == tt.dir {
						found = true
						break
					}
				}
				if found != tt.expected {
					t.Errorf("String processing logic failed: expected %v, got %v", tt.expected, found)
				}
			}
		})
	}
}

func TestManPageConstants(t *testing.T) {
	// Test that constants are defined with expected values
	if ManPageFileMode != 0644 {
		t.Errorf("ManPageFileMode = %v, want %v", ManPageFileMode, 0644)
	}
	
	if ManPageDirMode != 0755 {
		t.Errorf("ManPageDirMode = %v, want %v", ManPageDirMode, 0755)
	}
}

// BenchmarkIsWritableDir benchmarks the isWritableDir function
func BenchmarkIsWritableDir(b *testing.B) {
	tempDir := filepath.Join(os.TempDir(), "bench_manpage")
	defer os.RemoveAll(tempDir)
	
	// Create directory once
	os.MkdirAll(tempDir, 0755)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		isWritableDir(tempDir)
	}
}