package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const maxEnvironmentLineBytes = 64 * 1024

func systemEnvironment(key string) (string, bool) {
	return os.LookupEnv(key)
}

func readEnvironmentFiles(paths ...string) (map[string]string, error) {
	values := make(map[string]string)
	for _, path := range paths {
		fileValues, err := readEnvironmentFile(path)
		if err != nil {
			return nil, err
		}
		for key, value := range fileValues {
			values[key] = value
		}
	}

	return values, nil
}

func readEnvironmentFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open environment file %q: %w", path, err)
	}
	defer file.Close()

	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), maxEnvironmentLineBytes)

	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		key, value, skip, err := parseEnvironmentLine(scanner.Text())
		if err != nil {
			return nil, fmt.Errorf(
				"parse environment file %q line %d: %w",
				path,
				lineNumber,
				err,
			)
		}
		if skip {
			continue
		}
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf(
				"parse environment file %q line %d: duplicate key %s",
				path,
				lineNumber,
				key,
			)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read environment file %q: %w", path, err)
	}

	return values, nil
}

func parseEnvironmentLine(line string) (key string, value string, skip bool, err error) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", true, nil
	}

	line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
	key, rawValue, found := strings.Cut(line, "=")
	if !found {
		return "", "", false, fmt.Errorf("expected KEY=VALUE")
	}

	key = strings.TrimSpace(key)
	if !isEnvironmentKey(key) {
		return "", "", false, fmt.Errorf("invalid key %q", key)
	}

	value, err = parseEnvironmentValue(strings.TrimSpace(rawValue))
	if err != nil {
		return "", "", false, fmt.Errorf("invalid value for %s: %w", key, err)
	}

	return key, value, false, nil
}

func parseEnvironmentValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}

	switch value[0] {
	case '"':
		end := strings.LastIndex(value[1:], `"`)
		if end < 0 {
			return "", fmt.Errorf("unterminated double-quoted value")
		}
		end++
		if !onlyCommentFollows(value[end+1:]) {
			return "", fmt.Errorf("unexpected content after quoted value")
		}

		unquoted, err := strconv.Unquote(value[:end+1])
		if err != nil {
			return "", err
		}
		return unquoted, nil
	case '\'':
		end := strings.LastIndexByte(value[1:], '\'')
		if end < 0 {
			return "", fmt.Errorf("unterminated single-quoted value")
		}
		end++
		if !onlyCommentFollows(value[end+1:]) {
			return "", fmt.Errorf("unexpected content after quoted value")
		}

		return value[1:end], nil
	default:
		return stripInlineComment(value), nil
	}
}

func isEnvironmentKey(key string) bool {
	if key == "" || !isASCIILetterOrUnderscore(key[0]) {
		return false
	}
	for index := 1; index < len(key); index++ {
		character := key[index]
		if !isASCIILetterOrUnderscore(character) &&
			(character < '0' || character > '9') {
			return false
		}
	}

	return true
}

func isASCIILetterOrUnderscore(character byte) bool {
	return character == '_' ||
		(character >= 'a' && character <= 'z') ||
		(character >= 'A' && character <= 'Z')
}

func onlyCommentFollows(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || strings.HasPrefix(value, "#")
}

func stripInlineComment(value string) string {
	for index := 1; index < len(value); index++ {
		if value[index] == '#' && (value[index-1] == ' ' || value[index-1] == '\t') {
			return strings.TrimSpace(value[:index])
		}
	}

	return strings.TrimSpace(value)
}
