//go:build ignore

package main

import (
	"bytes"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
)

func main() {
	write, roots := parseArgs(os.Args[1:])
	if len(roots) == 0 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/check-format.go [-write] <path>...")
		os.Exit(2)
	}

	unformatted := false
	for _, root := range roots {
		if err := walk(root, write, &unformatted); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}

	if unformatted {
		os.Exit(1)
	}
}

func parseArgs(args []string) (bool, []string) {
	if len(args) > 0 && args[0] == "-write" {
		return true, args[1:]
	}

	return false, args
}

func walk(root string, write bool, unformatted *bool) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".go" {
			return nil
		}

		source, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}

		formatted, err := format.Source(source)
		if err != nil {
			return fmt.Errorf("format %s: %w", path, err)
		}
		if bytes.Equal(source, formatted) {
			return nil
		}

		if !write {
			fmt.Println(path)
			*unformatted = true
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", path, err)
		}
		if err := os.WriteFile(path, formatted, info.Mode().Perm()); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}

		return nil
	})
}
