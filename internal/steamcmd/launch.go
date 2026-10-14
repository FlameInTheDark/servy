package steamcmd

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var launchOptionKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// ResolveLaunchOptions turns the values supplied for one server into argv
// elements. Flags and values are separate unless flag_separator explicitly joins
// them into one element; callers must not concatenate these into a shell command.
func ResolveLaunchOptions(options []LaunchOption, values map[string]string, context LaunchContext) ([]string, map[string]string, error) {
	if err := validateLaunchOptions(options); err != nil {
		return nil, nil, err
	}
	known := make(map[string]LaunchOption, len(options))
	for _, option := range options {
		known[option.Key] = option
	}
	unknown := make([]string, 0)
	for key := range values {
		if _, ok := known[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, nil, fmt.Errorf("unknown launch option %q", unknown[0])
	}

	args := make([]string, 0, len(options)*2)
	resolved := make(map[string]string, len(options))
	for _, option := range options {
		value, supplied := values[option.Key]
		if option.Source != "" {
			if supplied {
				return nil, nil, fmt.Errorf("launch option %q is supplied by Servy and must not be set directly", option.Key)
			}
			var err error
			value, err = launchSourceValue(option.Source, context)
			if err != nil {
				return nil, nil, fmt.Errorf("launch option %q: %w", option.Key, err)
			}
		} else if !supplied || value == "" {
			value = option.Default
			if value == "" {
				if option.Required {
					return nil, nil, fmt.Errorf("launch option %q is required", option.Label)
				}
				continue
			}
		}

		if option.Required && strings.TrimSpace(value) == "" {
			return nil, nil, fmt.Errorf("launch option %q is required", option.Label)
		}
		if err := validateLaunchOptionValue(option, value); err != nil {
			return nil, nil, fmt.Errorf("launch option %q: %w", option.Label, err)
		}
		if option.Source == "" {
			resolved[option.Key] = value
		}

		if option.Type == "boolean" {
			appendBooleanOption(&args, option, value == "true")
			continue
		}
		appendFlagValue(&args, option, value)
	}
	return args, resolved, nil
}

func appendFlagValue(args *[]string, option LaunchOption, value string) {
	if option.Flag == "" {
		*args = append(*args, value)
		return
	}
	if option.FlagSeparator != "" {
		*args = append(*args, option.Flag+option.FlagSeparator+value)
		return
	}
	*args = append(*args, option.Flag, value)
}

func appendBooleanOption(args *[]string, option LaunchOption, enabled bool) {
	mappedValue := option.FalseValue
	if enabled {
		mappedValue = option.TrueValue
	}
	if option.Flag != "" {
		if enabled || mappedValue != "" {
			if mappedValue != "" && option.FlagSeparator != "" {
				*args = append(*args, option.Flag+option.FlagSeparator+mappedValue)
			} else {
				*args = append(*args, option.Flag)
				if mappedValue != "" {
					*args = append(*args, mappedValue)
				}
			}
		}
		return
	}
	if mappedValue == "" {
		mappedValue = strconv.FormatBool(enabled)
	}
	*args = append(*args, mappedValue)
}

// SourceValue returns the value Servy derives itself for a manifest option that
// declares a `source` (server name, ports, save directory). The panel uses it to
// show what automatic launch parameters will contain.
func SourceValue(source string, context LaunchContext) (string, error) {
	return launchSourceValue(source, context)
}

func launchSourceValue(source string, context LaunchContext) (string, error) {
	switch source {
	case "server_name":
		return context.ServerName, nil
	case "port":
		if context.Port < 1 || context.Port > 65535 {
			return "", fmt.Errorf("server port is out of range")
		}
		return strconv.Itoa(context.Port), nil
	case "query_port":
		if context.QueryPort < 1 || context.QueryPort > 65535 {
			return "", fmt.Errorf("query port is out of range")
		}
		return strconv.Itoa(context.QueryPort), nil
	case "save_dir":
		if context.SaveDir == "" {
			return "", fmt.Errorf("save directory is unavailable")
		}
		return context.SaveDir, nil
	default:
		return "", fmt.Errorf("unsupported source %q", source)
	}
}

func validateLaunchOptions(options []LaunchOption) error {
	if len(options) > 64 {
		return fmt.Errorf("launch_options may contain at most 64 entries")
	}
	seen := make(map[string]bool, len(options))
	for i, option := range options {
		where := fmt.Sprintf("launch_options[%d]", i)
		if !launchOptionKeyPattern.MatchString(option.Key) {
			return fmt.Errorf("%s.key must start with a lowercase letter and contain only lowercase letters, numbers, or underscores", where)
		}
		if seen[option.Key] {
			return fmt.Errorf("%s.key %q is duplicated", where, option.Key)
		}
		seen[option.Key] = true
		if strings.TrimSpace(option.Label) == "" || utf8.RuneCountInString(option.Label) > 100 {
			return fmt.Errorf("%s.label is required and must be at most 100 characters", where)
		}
		if utf8.RuneCountInString(option.Description) > 500 {
			return fmt.Errorf("%s.description must be at most 500 characters", where)
		}
		switch option.Type {
		case "text", "password", "number", "boolean", "select":
		default:
			return fmt.Errorf("%s.type must be text, password, number, boolean, or select", where)
		}
		if len(option.Flag) > 128 || hasWhitespaceOrControl(option.Flag) {
			return fmt.Errorf("%s.flag must be a single argument with no whitespace or control characters", where)
		}
		if utf8.RuneCountInString(option.FlagSeparator) > 16 || hasWhitespaceOrControl(option.FlagSeparator) {
			return fmt.Errorf("%s.flag_separator must be at most 16 characters with no whitespace or control characters", where)
		}
		if option.FlagSeparator != "" && option.Flag == "" {
			return fmt.Errorf("%s.flag_separator requires flag", where)
		}
		if option.Source != "" {
			expectedType := map[string]string{
				"server_name": "text",
				"port":        "number",
				"query_port":  "number",
				"save_dir":    "text",
			}[option.Source]
			if expectedType == "" {
				return fmt.Errorf("%s.source must be server_name, port, query_port, or save_dir", where)
			}
			if option.Type != expectedType {
				return fmt.Errorf("%s.type must be %s for source %q", where, expectedType, option.Source)
			}
			if option.Default != "" {
				return fmt.Errorf("%s.default cannot be set when source is used", where)
			}
		}
		if option.Type != "number" && (option.Min != nil || option.Max != nil) {
			return fmt.Errorf("%s.min and max are only valid for number options", where)
		}
		if option.Min != nil && option.Max != nil && *option.Min > *option.Max {
			return fmt.Errorf("%s.min must not exceed max", where)
		}
		if option.Type != "text" && option.Type != "password" && (option.MinLength != nil || option.MaxLength != nil) {
			return fmt.Errorf("%s.min_length and max_length are only valid for text or password options", where)
		}
		if option.MinLength != nil && (*option.MinLength < 0 || *option.MinLength > 4096) {
			return fmt.Errorf("%s.min_length must be between 0 and 4096", where)
		}
		if option.MaxLength != nil && (*option.MaxLength < 0 || *option.MaxLength > 4096) {
			return fmt.Errorf("%s.max_length must be between 0 and 4096", where)
		}
		if option.MinLength != nil && option.MaxLength != nil && *option.MinLength > *option.MaxLength {
			return fmt.Errorf("%s.min_length must not exceed max_length", where)
		}
		if option.Type != "boolean" && (option.TrueValue != "" || option.FalseValue != "") {
			return fmt.Errorf("%s.true_value and false_value are only valid for boolean options", where)
		}
		if hasWhitespaceOrControl(option.TrueValue) || hasWhitespaceOrControl(option.FalseValue) ||
			len(option.TrueValue) > 256 || len(option.FalseValue) > 256 {
			return fmt.Errorf("%s.true_value and false_value must be single values without control characters", where)
		}
		if option.Type == "select" {
			if len(option.Options) == 0 || len(option.Options) > 64 {
				return fmt.Errorf("%s.options must contain between 1 and 64 choices", where)
			}
			choiceValues := make(map[string]bool, len(option.Options))
			for j, choice := range option.Options {
				if choice.Value == "" || len(choice.Value) > 256 || hasWhitespaceOrControl(choice.Value) {
					return fmt.Errorf("%s.options[%d].value must be a non-empty single value", where, j)
				}
				if strings.TrimSpace(choice.Label) == "" || utf8.RuneCountInString(choice.Label) > 100 {
					return fmt.Errorf("%s.options[%d].label is required and must be at most 100 characters", where, j)
				}
				if choiceValues[choice.Value] {
					return fmt.Errorf("%s.options contains duplicate value %q", where, choice.Value)
				}
				choiceValues[choice.Value] = true
			}
		} else if len(option.Options) > 0 {
			return fmt.Errorf("%s.options is only valid for select options", where)
		}
		if option.Default != "" {
			if err := validateLaunchOptionValue(option, option.Default); err != nil {
				return fmt.Errorf("%s.default: %w", where, err)
			}
		}
	}
	return nil
}

func validateLaunchOptionValue(option LaunchOption, value string) error {
	if hasControlCharacter(value) {
		return fmt.Errorf("value must not contain NUL, line breaks, or other control characters")
	}
	if utf8.RuneCountInString(value) > 4096 {
		return fmt.Errorf("value must be no longer than 4096 characters")
	}
	switch option.Type {
	case "text", "password":
		length := utf8.RuneCountInString(value)
		if option.MinLength != nil && length < *option.MinLength {
			return fmt.Errorf("must be at least %d characters", *option.MinLength)
		}
		if option.MaxLength != nil && length > *option.MaxLength {
			return fmt.Errorf("must be no longer than %d characters", *option.MaxLength)
		}
	case "number":
		n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			return fmt.Errorf("must be a whole number")
		}
		if option.Min != nil && n < int64(*option.Min) {
			return fmt.Errorf("must be at least %d", *option.Min)
		}
		if option.Max != nil && n > int64(*option.Max) {
			return fmt.Errorf("must be no greater than %d", *option.Max)
		}
	case "boolean":
		if value != "true" && value != "false" {
			return fmt.Errorf("must be true or false")
		}
	case "select":
		for _, choice := range option.Options {
			if choice.Value == value {
				return nil
			}
		}
		return fmt.Errorf("must match one of the declared choices")
	}
	return nil
}

func hasWhitespaceOrControl(value string) bool {
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func hasControlCharacter(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// EncodeGameSnapshot serializes the exact game manifest and collected launch
// values into the server row's definitions envelope.
func EncodeGameSnapshot(snapshot GameSnapshot) (*string, error) {
	if err := ValidateGame(snapshot.Game); err != nil {
		return nil, fmt.Errorf("invalid game snapshot: %w", err)
	}
	data, err := json.Marshal(ServerDefinitions{SteamGame: &snapshot})
	if err != nil {
		return nil, err
	}
	value := string(data)
	return &value, nil
}

// GameSnapshotFromDefinitions retrieves the frozen game manifest for one
// server. Non-JSON legacy definitions are ignored for backwards compatibility.
func GameSnapshotFromDefinitions(raw *string) (*GameSnapshot, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*raw)
	if !strings.HasPrefix(trimmed, "{") {
		return nil, nil
	}
	var definitions ServerDefinitions
	if err := json.Unmarshal([]byte(trimmed), &definitions); err != nil {
		return nil, fmt.Errorf("parse server definitions: %w", err)
	}
	if definitions.SteamGame == nil {
		return nil, nil
	}
	if err := ValidateGame(definitions.SteamGame.Game); err != nil {
		return nil, fmt.Errorf("invalid stored game manifest: %w", err)
	}
	return definitions.SteamGame, nil
}
