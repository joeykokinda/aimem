package vault

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// # WHY AGENT WRITES ARE MARKED AND QUARANTINED
//
// Letting an agent write freely into a second brain is how it fills with junk. Letting it
// write nothing is how the brain stays empty. The way out is to notice *where* junk
// actually costs you: a bad note is harmless sitting on disk, and expensive only once it
// is in the index every session loads.
//
// So agents may write whatever they like, and every write is marked. Marked content is
// searchable and readable, but it is excluded from the generated index until a human
// promotes it. Junk therefore costs nothing per session, and triage is a thing you do
// when you feel like it rather than a gate on capture.
//
// Promotion is a one-word edit: drop the marker. Rejection is deleting a line.

// AgentMarker tags a line or a note as written by an agent and not yet reviewed.
const AgentMarker = "unreviewed"

// agentFactPattern matches a dated agent line in an Agent memory section.
var agentFactPattern = regexp.MustCompile(`^- (\d{4}-\d{2}-\d{2}) \(` + AgentMarker + `\): (.+)$`)

// Unreviewed reports whether a whole note is agent-written and still unpromoted.
func (n *Note) Unreviewed() bool {
	return strings.EqualFold(n.Fields["origin"], "agent") &&
		!strings.EqualFold(n.Fields["reviewed"], "true")
}

// UnreviewedFacts returns the agent-written facts in a note that nobody has promoted.
func (n *Note) UnreviewedFacts() []Fact {
	var facts []Fact
	for index, line := range n.Lines {
		if match := agentFactPattern.FindStringSubmatch(strings.TrimSpace(line)); match != nil {
			facts = append(facts, Fact{
				Note: n, Line: index + 1, Date: match[1], Text: match[2],
			})
		}
	}
	return facts
}

// Fact is one agent-written line awaiting review.
type Fact struct {
	Note *Note
	Line int
	Date string
	Text string
}

// RememberAs appends a fact to a note's agent-memory section.
//
// When the writer is an agent the line carries a marker, which is what keeps it out of
// the generated index until reviewed. A human writing the same fact does not need the
// marker: they already made the judgement the review step exists to make.
func RememberAs(note *Note, fact string, byAgent bool) error {
	fact = strings.TrimSpace(fact)
	if fact == "" {
		return fmt.Errorf("nothing to remember")
	}
	if name := MatchSecret(fact); name != "" {
		return fmt.Errorf("refusing to write: this looks like a %s, and the vault is pushed to a remote", name)
	}

	existing, err := os.ReadFile(note.Abs)
	if err != nil {
		return err
	}
	body := strings.TrimRight(string(existing), "\n")

	// Writing the same fact twice is the most common way an agent creates junk: it
	// re-derives something it already recorded in an earlier session.
	if strings.Contains(strings.ToLower(body), strings.ToLower(fact)) {
		return fmt.Errorf("already recorded in %s", note.Path)
	}

	if !strings.Contains(body, AgentMemoryHeading) {
		body += "\n\n" + AgentMemoryHeading
	}
	stamp := time.Now().Format("2006-01-02")
	if byAgent {
		body += fmt.Sprintf("\n- %s (%s): %s\n", stamp, AgentMarker, fact)
	} else {
		body += fmt.Sprintf("\n- %s: %s\n", stamp, fact)
	}
	return os.WriteFile(note.Abs, []byte(body), 0o644)
}

// PromoteFacts removes the marker from every agent line in a note, which is what moves
// them into the index.
func PromoteFacts(note *Note) (int, error) {
	raw, err := os.ReadFile(note.Abs)
	if err != nil {
		return 0, err
	}
	var promoted int
	lines := strings.Split(string(raw), "\n")
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if match := agentFactPattern.FindStringSubmatch(trimmed); match != nil {
			lines[index] = fmt.Sprintf("- %s: %s", match[1], match[2])
			promoted++
		}
	}
	if promoted == 0 {
		return 0, nil
	}
	return promoted, os.WriteFile(note.Abs, []byte(strings.Join(lines, "\n")), 0o644)
}

// DropFacts deletes every unreviewed agent line from a note.
func DropFacts(note *Note) (int, error) {
	raw, err := os.ReadFile(note.Abs)
	if err != nil {
		return 0, err
	}
	var kept []string
	var dropped int
	for _, line := range strings.Split(string(raw), "\n") {
		if agentFactPattern.MatchString(strings.TrimSpace(line)) {
			dropped++
			continue
		}
		kept = append(kept, line)
	}
	if dropped == 0 {
		return 0, nil
	}
	return dropped, os.WriteFile(note.Abs, []byte(strings.Join(kept, "\n")), 0o644)
}

// MarkReviewed flips a whole agent-created note to reviewed, so it enters the index.
func MarkReviewed(note *Note) error {
	raw, err := os.ReadFile(note.Abs)
	if err != nil {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	for index, line := range lines {
		if index == 0 {
			continue
		}
		if strings.HasPrefix(line, "---") {
			// End of frontmatter: insert before it.
			lines = append(lines[:index], append([]string{"reviewed: true"}, lines[index:]...)...)
			return os.WriteFile(note.Abs, []byte(strings.Join(lines, "\n")), 0o644)
		}
		if key, _, found := strings.Cut(line, ":"); found && strings.TrimSpace(key) == "reviewed" {
			lines[index] = "reviewed: true"
			return os.WriteFile(note.Abs, []byte(strings.Join(lines, "\n")), 0o644)
		}
	}
	return fmt.Errorf("no frontmatter block in %s", note.Path)
}
