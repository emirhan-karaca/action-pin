package pinner

import (
	"bytes"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

type sourceEdit struct {
	node    yaml.Node
	value   string
	comment string
}

// applySourceEdits uses parser positions to replace single-line action scalars.
// Nothing outside those scalars and their line annotations is reserialized.
// Exotic scalar syntax falls back to the YAML encoder in ProcessContent.
func applySourceEdits(content []byte, edits []sourceEdit) ([]byte, bool) {
	lines := strings.Split(string(content), "\n")
	sort.SliceStable(edits, func(i, j int) bool {
		if edits[i].node.Line != edits[j].node.Line {
			return edits[i].node.Line > edits[j].node.Line
		}
		return edits[i].node.Column > edits[j].node.Column
	})
	for _, edit := range edits {
		n := edit.node
		if n.Kind != yaml.ScalarNode || n.Line < 1 || n.Line > len(lines) || n.Anchor != "" || n.Style&yaml.TaggedStyle != 0 {
			return nil, false
		}
		oldValue, newValue := n.Value, edit.value
		switch n.Style {
		case 0:
		case yaml.SingleQuotedStyle:
			oldValue = "'" + strings.ReplaceAll(oldValue, "'", "''") + "'"
			newValue = "'" + strings.ReplaceAll(newValue, "'", "''") + "'"
		case yaml.DoubleQuotedStyle:
			// Let yaml.v3 escape the replacement; the original may use other
			// escape spellings, in which case the exact source check fails safely.
			var ok bool
			oldValue, ok = quotedScalar(n.Value)
			if !ok {
				return nil, false
			}
			newValue, ok = quotedScalar(edit.value)
			if !ok {
				return nil, false
			}
		default:
			return nil, false
		}
		if strings.ContainsAny(oldValue, "\r\n") {
			return nil, false
		}
		line := lines[n.Line-1]
		// YAML columns count Unicode characters, not UTF-8 bytes. A BOM does
		// not contribute to the first line's parser column.
		start := 0
		if n.Line == 1 && strings.HasPrefix(line, "\ufeff") {
			start = len("\ufeff")
		}
		for column := 1; column < n.Column && start < len(line); column++ {
			_, size := utf8.DecodeRuneInString(line[start:])
			start += size
		}
		if !strings.HasPrefix(line[start:], oldValue) {
			return nil, false
		}
		line = line[:start] + newValue + line[start+len(oldValue):]
		// Append after the complete line so flow mappings and sequences keep
		// their closing delimiters, and existing comments remain verbatim.
		end := len(strings.TrimRight(line, " \t\r"))
		annotation := " " + edit.comment
		if n.LineComment != "" {
			annotation = "; " + strings.TrimPrefix(edit.comment, "# ")
		}
		lines[n.Line-1] = line[:end] + annotation + line[end:]
	}
	return []byte(strings.Join(lines, "\n")), true
}

func quotedScalar(value string) (string, bool) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	node := yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Style: yaml.DoubleQuotedStyle, Value: value}
	if err := enc.Encode(&node); err != nil {
		return "", false
	}
	if err := enc.Close(); err != nil {
		return "", false
	}
	return strings.TrimSuffix(buf.String(), "\n"), true
}
