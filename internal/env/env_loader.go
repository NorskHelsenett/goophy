package env

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// LoadEnv initializes the environment variables with the following precedence:
// 1. Existing OS environment variables (already take precedence due to how LoadEnvFile works)
// 2. Custom environment file if specified (envFilePath)
// 3. Default .env file in the current directory
// Returns any error that occurred during loading custom env file only
func LoadEnv(envFilePath string) error {
	var err error

	// If a custom env file was specified, try to load it
	if envFilePath != "" {
		err = loadEnvFile(envFilePath, true) // Return error if this file doesn't exist
	}

	// Always try to load the default .env file (but don't report errors)
	// This will only set variables that aren't already set
	if _, statErr := os.Stat(".env"); statErr == nil {
		_ = loadEnvFile(".env", false)
	}

	return err
}

// LoadEnvOptional wraps LoadEnv to simplify conditional loading when a path may be empty.
// If path is empty it only attempts to load the default .env (same as calling LoadEnv("")).
// Returns any error from loading the provided env file.
func LoadEnvOptional(path string) error {
	// Reuse existing logic: LoadEnv already handles empty string gracefully.
	return LoadEnv(path)
}

// GetEnv retrieves an environment variable value with fallback to default
func GetEnv(key, defaultValue string) string {
	value := strings.Trim(os.Getenv(key), `"'`)
	if value == "" {
		return defaultValue
	}
	return value
}

// loadEnvFile loads environment variables from a .env file
// If reportError is false, it will silently ignore if the file doesn't exist
func loadEnvFile(filePath string, reportError bool) error {
	// Check if file exists
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		if reportError {
			return fmt.Errorf("env file does not exist: %s", filePath)
		}
		return nil
	}

	// Open the file
	file, err := os.Open(filePath)
	if err != nil {
		if reportError {
			return err
		}
		return nil
	}
	defer file.Close()

	// Read line by line
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments
		if len(line) == 0 || strings.HasPrefix(line, "#") {
			continue
		}

		// Split by first equals sign
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			if reportError {
				return fmt.Errorf("invalid format in %s at line %d", filePath, lineNum)
			}
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Remove quotes if present
		value = strings.Trim(value, `"'`)

		// Only set if not already in environment - this ensures OS env variables take precedence
		if os.Getenv(key) == "" {
			os.Setenv(key, value)
		}
	}

	if err := scanner.Err(); err != nil && reportError {
		return err
	}

	return nil
}
