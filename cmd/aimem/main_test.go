package main

import (
	"flag"
	"reflect"
	"testing"
)

// TestPermute covers the parsing bug where `aimem find hedera --json` silently returned
// nothing: Go's flag package stops at the first positional, so "--json" became a third
// search term that matched nothing and failed the conjunctive query.
func TestPermute(t *testing.T) {
	build := func() *flag.FlagSet {
		set := flag.NewFlagSet("test", flag.ContinueOnError)
		set.String("vault", "", "")
		set.String("type", "", "")
		set.Int("limit", 0, "")
		set.Bool("json", false, "")
		return set
	}

	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"flags first", []string{"--limit", "3", "hedera"},
			[]string{"--limit", "3", "--", "hedera"}},
		{"flags after positionals", []string{"hedera", "--limit", "3"},
			[]string{"--limit", "3", "--", "hedera"}},
		{"bool flag consumes nothing", []string{"hedera", "--json"},
			[]string{"--json", "--", "hedera"}},
		{"interleaved", []string{"hedera", "--type", "project", "consensus", "--json"},
			[]string{"--type", "project", "--json", "--", "hedera", "consensus"}},
		{"equals form", []string{"hedera", "--limit=3"},
			[]string{"--limit=3", "--", "hedera"}},
		{"only flags", []string{"--json"}, []string{"--json"}},
		{"only positionals", []string{"a", "b"}, []string{"--", "a", "b"}},
		{"double dash protects a dashed term", []string{"--json", "--", "-weird-term"},
			[]string{"--json", "--", "-weird-term"}},
		{"empty", nil, nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := permute(build(), test.args)
			if !reflect.DeepEqual(got, test.want) {
				t.Errorf("permute(%v) = %v, want %v", test.args, got, test.want)
			}
		})
	}
}

// TestPermutedArgsParse is the end of the same story: after permuting, the flag set must
// actually see the flags and leave the search terms as positionals.
func TestPermutedArgsParse(t *testing.T) {
	set := flag.NewFlagSet("find", flag.ContinueOnError)
	limit := set.Int("limit", 0, "")
	asJSON := set.Bool("json", false, "")

	if err := set.Parse(permute(set, []string{"hedera", "consensus", "--limit", "5", "--json"})); err != nil {
		t.Fatal(err)
	}
	if *limit != 5 || !*asJSON {
		t.Errorf("limit = %d, json = %v; want 5, true", *limit, *asJSON)
	}
	if got := set.Args(); !reflect.DeepEqual(got, []string{"hedera", "consensus"}) {
		t.Errorf("terms = %v, want [hedera consensus]", got)
	}
}
