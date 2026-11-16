package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"unicode"
)

func ExpandConfigArgs(args []string) ([]string, error) {
	files := 0
	return expandConfigArgsDepth(args, 0, &files)
}

func expandConfigArgsDepth(args []string, depth int, files *int) ([]string, error) {
	if depth > 10 {
		return nil, fmt.Errorf("config files nested too deeply")
	}
	if *files > 512 {
		return nil, fmt.Errorf("too many config files")
	}
	var out []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		var file string
		switch {
		case arg == "--config" || arg == "-K":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("option %s: requires parameter", arg)
			}
			i++
			file = args[i]
		case strings.HasPrefix(arg, "--config="):
			file = strings.TrimPrefix(arg, "--config=")
		case strings.HasPrefix(arg, "-K") && len(arg) > 2:
			file = arg[2:]
		default:
			out = append(out, arg)
			continue
		}
		*files++
		fileArgs, err := readConfigFile(file)
		if err != nil {
			return nil, err
		}
		expanded, err := expandConfigArgsDepth(fileArgs, depth+1, files)
		if err != nil {
			return nil, err
		}
		out = append(out, expanded...)
	}
	return out, nil
}

func readConfigFile(path string) ([]string, error) {
	var f *os.File
	var err error
	if path == "-" {
		f = os.Stdin
	} else {
		f, err = os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("option --config: %w", err)
		}
		defer f.Close()
	}

	var args []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	for scanner.Scan() {
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
			args = append(args, "--config")
			args = append(args, value)
			continue
		}
		if len(name) == 1 {
			args = append(args, "-"+name)
			if hasValue && value != "" {
				args = append(args, value)
			}
			continue
		}
		if hasValue {
			args = append(args, "--"+name+"="+value)
		} else {
			args = append(args, "--"+name)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return args, nil
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
