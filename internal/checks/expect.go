package checks

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/restoredrill/restoredrill/internal/config"
)

func sprintf(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// matchScalar applies the numeric/string expectation keys to a scalar value.
// It returns (ok, message). Relevant keys: min, max, equals.
func matchScalar(e config.Expect, value float64, raw string) (bool, string) {
	if e.Min != nil && value < *e.Min {
		return false, sprintf("value %s is below min %s", raw, trimFloat(*e.Min))
	}
	if e.Max != nil && value > *e.Max {
		return false, sprintf("value %s is above max %s", raw, trimFloat(*e.Max))
	}
	if e.HasKey("equals") && raw != e.Equals {
		return false, sprintf("value %q does not equal %q", raw, e.Equals)
	}
	return true, ""
}

// matchText applies equals/contains/regex to a text body.
func matchText(e config.Expect, body string) (bool, string) {
	if e.HasKey("equals") && body != e.Equals {
		return false, "body does not equal expected value"
	}
	if e.Contains != "" && !strings.Contains(body, e.Contains) {
		return false, sprintf("body does not contain %q", e.Contains)
	}
	if e.Regex != "" {
		re, err := regexp.Compile(e.Regex)
		if err != nil {
			return false, sprintf("invalid regex: %v", err)
		}
		if !re.MatchString(body) {
			return false, sprintf("body does not match regex %q", e.Regex)
		}
	}
	return true, ""
}

// matchJSON applies a JSON expectation using a simple JSONPath subset
// ($.field.subfield[0]). Returns (ok, message).
func matchJSON(je *config.JSONExpect, body string) (bool, string) {
	if je == nil {
		return true, ""
	}
	var doc any
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		return false, fmt.Sprintf("response is not valid JSON: %v", err)
	}
	val, found := jsonPath(doc, je.Path)
	if je.Exists != nil {
		if *je.Exists && !found {
			return false, sprintf("json path %s does not exist", je.Path)
		}
		if !*je.Exists && found {
			return false, sprintf("json path %s exists but should not", je.Path)
		}
	}
	if je.HasKey("equals") {
		if !found {
			return false, sprintf("json path %s does not exist", je.Path)
		}
		if scalarString(val) != je.Equals {
			return false, sprintf("json path %s = %q, want %q", je.Path, scalarString(val), je.Equals)
		}
	}
	return true, ""
}

// jsonPath walks a decoded JSON document with a $.a.b[0] style path.
func jsonPath(doc any, path string) (any, bool) {
	path = strings.TrimPrefix(path, "$")
	path = strings.TrimPrefix(path, ".")
	if path == "" {
		return doc, true
	}
	cur := doc
	// Tokenize into field names and [index] segments.
	for _, tok := range tokenizePath(path) {
		if strings.HasPrefix(tok, "[") && strings.HasSuffix(tok, "]") {
			idx, err := strconv.Atoi(tok[1 : len(tok)-1])
			if err != nil {
				return nil, false
			}
			arr, ok := cur.([]any)
			if !ok || idx < 0 || idx >= len(arr) {
				return nil, false
			}
			cur = arr[idx]
		} else {
			obj, ok := cur.(map[string]any)
			if !ok {
				return nil, false
			}
			v, ok := obj[tok]
			if !ok {
				return nil, false
			}
			cur = v
		}
	}
	return cur, true
}

func tokenizePath(path string) []string {
	var toks []string
	var cur strings.Builder
	for i := 0; i < len(path); i++ {
		switch path[i] {
		case '.':
			if cur.Len() > 0 {
				toks = append(toks, cur.String())
				cur.Reset()
			}
		case '[':
			if cur.Len() > 0 {
				toks = append(toks, cur.String())
				cur.Reset()
			}
			j := strings.IndexByte(path[i:], ']')
			if j < 0 {
				return toks
			}
			toks = append(toks, path[i:i+j+1])
			i += j
		default:
			cur.WriteByte(path[i])
		}
	}
	if cur.Len() > 0 {
		toks = append(toks, cur.String())
	}
	return toks
}

func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return trimFloat(t)
	case bool:
		return strconv.FormatBool(t)
	case nil:
		return "null"
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}