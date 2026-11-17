package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

const (
	maxConfigDepth = 10
	maxConfigFiles = 512
	maxConfigBytes = 8 << 20
	maxConfigLines = 100000
	maxConfigArgs  = 100000
)

type configBudget struct {
	files int
	bytes int64
	lines int
	args  int
}

func (b *configBudget) addArgs(n int) error {
	b.args += n
	if b.args > maxConfigArgs {
		return fmt.Errorf("config files expand to too many arguments")
	}
	return nil
}

func ExpandConfigArgs(args []string) ([]string, error) {
	baseDir, err := os.Getwd()
	if err != nil {
		baseDir = "."
	}
	return expandConfigArgsDepth(args, 0, baseDir, "", &configBudget{})
}

func expandConfigArgsDepth(args []string, depth int, baseDir, rootDir string, budget *configBudget) ([]string, error) {
	if depth > maxConfigDepth {
		return nil, fmt.Errorf("config files nested too deeply")
	}
	var out []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			out = append(out, args[i:]...)
			if err := budget.addArgs(len(args) - i); err != nil {
				return nil, err
			}
			break
		}
		if path, needsValue, ok := configOption(arg); ok {
			if needsValue {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("option %s: requires parameter", arg)
				}
				i++
				path = args[i]
			}
			if path == "" {
				return nil, fmt.Errorf("option --config: requires parameter")
			}
			resolved, fileBase, fileRoot, err := resolveConfigPath(path, depth, baseDir, rootDir)
			if err != nil {
				return nil, err
			}
			budget.files++
			if budget.files > maxConfigFiles {
				return nil, fmt.Errorf("too many config files")
			}
			fileArgs, err := readConfigFile(resolved, fileBase, fileRoot, budget)
			if err != nil {
				return nil, err
			}
			expanded, err := expandConfigArgsDepth(fileArgs, depth+1, fileBase, fileRoot, budget)
			if err != nil {
				return nil, err
			}
			out = append(out, expanded...)
			continue
		}
		if argConsumesNextValue(arg) {
			out = append(out, arg)
			count := 1
			if i+1 < len(args) {
				i++
				out = append(out, args[i])
				count = 2
			}
			if err := budget.addArgs(count); err != nil {
				return nil, err
			}
			continue
		}
		out = append(out, arg)
		if err := budget.addArgs(1); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func configOption(arg string) (path string, needsValue bool, ok bool) {
	switch {
	case arg == "--config" || arg == "-K":
		return "", true, true
	case strings.HasPrefix(arg, "--config="):
		return strings.TrimPrefix(arg, "--config="), false, true
	case strings.HasPrefix(arg, "-K") && len(arg) > 2:
		return arg[2:], false, true
	default:
		return "", false, false
	}
}

func argConsumesNextValue(arg string) bool {
	if strings.HasPrefix(arg, "--") {
		name := strings.TrimPrefix(arg, "--")
		if name == "" || strings.Contains(name, "=") {
			return false
		}
		_, ok := longValueOptions[name]
		return ok
	}
	if len(arg) > 1 && arg[0] == '-' && arg != "--" {
		for pos := 1; pos < len(arg); pos++ {
			long, ok := shortOptionNames[arg[pos]]
			if !ok {
				return false
			}
			if _, ok := longValueOptions[long]; ok {
				return pos == len(arg)-1
			}
		}
	}
	return false
}

func resolveConfigPath(path string, depth int, baseDir, rootDir string) (resolved, fileBase, fileRoot string, err error) {
	if path == "-" {
		root := rootDir
		if root == "" {
			root = baseDir
		}
		return "-", baseDir, root, nil
	}
	if depth == 0 {
		if !filepath.IsAbs(path) {
			path = filepath.Join(baseDir, path)
		}
		path = filepath.Clean(path)
		return path, filepath.Dir(path), filepath.Dir(path), nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(baseDir, path)
	}
	path = filepath.Clean(path)
	if rootDir != "" {
		// Compare canonical paths so a symlinked intermediate directory
		// cannot carry an include outside the config root.
		resolvedRoot := rootDir
		if real, err := filepath.EvalSymlinks(rootDir); err == nil {
			resolvedRoot = real
		}
		resolvedPath := path
		if real, err := filepath.EvalSymlinks(path); err == nil {
			resolvedPath = real
		} else {
			if real, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
				resolvedPath = filepath.Join(real, filepath.Base(path))
			}
		}
		inside, relErr := pathWithin(resolvedRoot, resolvedPath)
		if relErr != nil {
			return "", "", "", relErr
		}
		if !inside {
			return "", "", "", fmt.Errorf("config include escapes config directory")
		}
	}
	return path, filepath.Dir(path), rootDir, nil
}

func pathWithin(root, path string) (bool, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false, err
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))), nil
}

func readConfigFile(path, baseDir, rootDir string, budget *configBudget) ([]string, error) {
	var f *os.File
	if path == "-" {
		f = os.Stdin
	} else {
		opened, err := openConfigFile(path)
		if err != nil {
			return nil, fmt.Errorf("option --config: %w", err)
		}
		defer opened.Close()
		f = opened
	}

	var args []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
		budget.bytes += int64(len(scanner.Bytes()) + 1)
		if budget.bytes > maxConfigBytes {
			return nil, fmt.Errorf("config files exceed %d bytes", maxConfigBytes)
		}
		budget.lines++
		if budget.lines > maxConfigLines {
			return nil, fmt.Errorf("config files exceed %d lines", maxConfigLines)
		}
		line := strings.TrimSpace(strings.TrimRight(scanner.Text(), "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name := line
		value := ""
		hasValue := false
		if idx := configSeparator(line); idx >= 0 {
			name = strings.TrimSpace(line[:idx])
			value = strings.TrimSpace(line[idx:])
			value = strings.TrimPrefix(value, "=")
			value = strings.TrimSpace(value)
			hasValue = true
		}
		name = strings.TrimLeft(name, "-")
		if name == "" {
			continue
		}
		value = unquoteConfigValue(value)

		if name == "config" || name == "K" {
			if value == "" {
				return nil, fmt.Errorf("option --config: requires parameter")
			}
			resolved, err := resolveNestedConfig(value, baseDir, rootDir)
			if err != nil {
				return nil, err
			}
			if err := budget.addArgs(2); err != nil {
				return nil, err
			}
			args = append(args, "--config", resolved)
			continue
		}
		if len(name) == 1 {
			count := 1
			args = append(args, "-"+name)
			if hasValue && value != "" {
				args = append(args, value)
				count = 2
			}
			if err := budget.addArgs(count); err != nil {
				return nil, err
			}
			continue
		}
		if hasValue {
			args = append(args, "--"+name+"="+value)
		} else {
			args = append(args, "--"+name)
		}
		if err := budget.addArgs(1); err != nil {
			return nil, err
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return args, nil
}

func resolveNestedConfig(value, baseDir, rootDir string) (string, error) {
	if value == "-" {
		return "", fmt.Errorf("nested config cannot read stdin")
	}
	if !filepath.IsAbs(value) {
		value = filepath.Join(baseDir, value)
	}
	value = filepath.Clean(value)
	if rootDir != "" {
		inside, err := pathWithin(rootDir, value)
		if err != nil {
			return "", err
		}
		if !inside {
			return "", fmt.Errorf("config include escapes config directory")
		}
	}
	return value, nil
}

func configSeparator(line string) int {
	for i, r := range line {
		if r == '=' || unicode.IsSpace(r) {
			return i
		}
	}
	return -1
}

func unquoteConfigValue(value string) string {
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
			value = strings.NewReplacer(
				`\\`, "\\",
				`\"`, `"`,
				`\'`, `'`,
				`\n`, "\n",
				`\r`, "\r",
				`\t`, "\t",
			).Replace(value)
		}
	}
	return value
}
