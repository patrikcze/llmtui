package config

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// checkQuotedInlineLists rejects a list setting whose value is a string that
// looks like an inline YAML list, such as `stop: "[]"`. Viper splits a string
// given for a list on commas, so that value would load as the one-item list
// ["[]"] rather than an empty list — for the embedded provider's stop strings
// it silently ended every reply at the first "[]". The quotes make the
// writer's intent ambiguous, so the setting is refused with its key named
// instead of guessed at. Every such key is reported at once. A real YAML
// list, including `["[/INST]"]`, and a plain string for a non-list setting
// are unaffected.
func checkQuotedInlineLists(settings map[string]any) error {
	var problems []error
	collectQuotedInlineLists(settings, reflect.TypeOf(Config{}), "", &problems)
	return errors.Join(problems...)
}

func collectQuotedInlineLists(raw any, typ reflect.Type, path string, problems *[]error) {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch typ.Kind() {
	case reflect.Struct:
		values, ok := raw.(map[string]any)
		if !ok {
			return
		}
		for i := range typ.NumField() {
			field := typ.Field(i)
			if !field.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(field.Tag.Get("mapstructure"), ",")
			if name == "-" {
				continue
			}
			if name == "" {
				name = strings.ToLower(field.Name)
			}
			value, present := values[name]
			if !present {
				continue
			}
			collectQuotedInlineLists(value, field.Type, joinSettingPath(path, name), problems)
		}
	case reflect.Map:
		values, ok := raw.(map[string]any)
		if !ok || typ.Key().Kind() != reflect.String {
			return
		}
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			collectQuotedInlineLists(values[key], typ.Elem(), joinSettingPath(path, key), problems)
		}
	case reflect.Slice:
		text, ok := raw.(string)
		if !ok {
			return
		}
		trimmed := strings.TrimSpace(text)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			*problems = append(*problems, fmt.Errorf("%s: got the string %q, which would load as a one-item list; write a YAML list instead (an empty list is [] without quotes)", path, text))
		}
	}
}

func joinSettingPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}
