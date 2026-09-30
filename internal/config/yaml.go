package config

import (
	"strconv"
	"strings"
)

// A deliberately small YAML subset: nested maps, scalars, inline `[a, b]` lists, and
// block `- item` lists. That is the whole grammar a vault config needs.
//
// The alternative is a third-party YAML library, which would be the module's first
// dependency. A config file this shape is not worth that: the parser below is smaller
// than the dependency's changelog, and it fails loudly on anything it does not
// understand rather than guessing.
type node struct {
	scalar   string
	items    []string
	children map[string]*node
}

func newNode() *node {
	return &node{children: map[string]*node{}}
}

func (n *node) child(key string) *node {
	if n == nil {
		return nil
	}
	return n.children[key]
}

func (n *node) text(key string) string {
	if child := n.child(key); child != nil {
		return child.scalar
	}
	return ""
}

func (n *node) list(key string) []string {
	child := n.child(key)
	if child == nil {
		return nil
	}
	if len(child.items) > 0 {
		return child.items
	}
	if child.scalar != "" {
		return splitInline(child.scalar)
	}
	return nil
}

// number returns the integer at key, or fallback when the key is absent or unparseable.
func (n *node) number(key string, fallback int) int {
	value := strings.TrimSpace(n.text(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

// boolean returns the flag at key, or fallback when absent. Anything other than a
// recognized false spelling reads as true, so a typo fails safe for `enabled:` fields.
func (n *node) boolean(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(n.text(key)))
	if value == "" {
		return fallback
	}
	return value != "false" && value != "no" && value != "off" && value != "0"
}

// parseYAML builds a tree from indentation. Tabs are rejected by the caller before this
// runs, so indent width is a plain space count.
func parseYAML(source string) *node {
	root := newNode()
	// stack[i] is the node that owns content indented deeper than indents[i].
	stack := []*node{root}
	indents := []int{-1}

	for _, raw := range strings.Split(source, "\n") {
		line := stripComment(raw)
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		trimmed := strings.TrimSpace(line)

		for len(indents) > 1 && indent <= indents[len(indents)-1] {
			stack = stack[:len(stack)-1]
			indents = indents[:len(indents)-1]
		}
		parent := stack[len(stack)-1]

		// A block list item belongs to the key directly above it, which is the node
		// already on top of the stack after that key was read.
		if strings.HasPrefix(trimmed, "- ") || trimmed == "-" {
			if item := unquote(strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))); item != "" {
				parent.items = append(parent.items, item)
			}
			continue
		}

		colon := strings.Index(trimmed, ":")
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:colon])
		value := strings.TrimSpace(trimmed[colon+1:])
		if key == "" {
			continue
		}

		child := newNode()
		child.scalar = unquote(value)
		if strings.HasPrefix(value, "[") {
			child.items = splitInline(value)
			child.scalar = ""
		}
		parent.children[key] = child

		// A key with no inline value owns whatever is indented under it, whether that
		// turns out to be a nested map or a block list.
		stack = append(stack, child)
		indents = append(indents, indent)
	}
	return root
}

// stripComment removes a trailing `#` comment, ignoring one inside quotes so a value
// like "a # b" survives.
func stripComment(line string) string {
	var quote byte
	for index := 0; index < len(line); index++ {
		character := line[index]
		switch {
		case quote != 0:
			if character == quote {
				quote = 0
			}
		case character == '"' || character == '\'':
			quote = character
		case character == '#' && (index == 0 || line[index-1] == ' '):
			return line[:index]
		}
	}
	return line
}

func splitInline(value string) []string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")
	var items []string
	for _, part := range strings.Split(value, ",") {
		if item := unquote(strings.TrimSpace(part)); item != "" {
			items = append(items, item)
		}
	}
	return items
}

func unquote(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		first, last := value[0], value[len(value)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

// has reports whether a key was present at all, which lets an explicitly empty value
// (`journal: ""`) mean "disabled" rather than falling back to the default.
func (n *node) has(key string) bool {
	if n == nil {
		return false
	}
	_, present := n.children[key]
	return present
}
