package operator

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestDigitRunsAreMaximal(t *testing.T) {
	got := digitRuns("12 hosts, web-1 and 007, x4y 1234")
	want := []string{"12", "1", "007", "4", "1234"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
	if len(digitRuns("none")) != 0 {
		t.Fatal("expected no runs")
	}
}

func TestIdentifiers(t *testing.T) {
	got := identifiers(`Host (web-1) is down; db_main, ghcr.io/x/y: and a-b plain words.`)
	want := []string{"web-1", "db_main", "ghcr.io/x/y"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestValidateBriefing(t *testing.T) {
	facts := BriefingFacts{Hosts: 2, Containers: 14, Running: 13, Items: []string{"db-1 down"}}
	rule := "2 hosts, 14 containers, 13 running. db-1 down"
	ok := "2 hosts, 14 containers (13 running). db-1 down"
	if err := validateBriefing(ok, facts, rule); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"empty":          "  ",
		"four lines":     "2\n14\n13\ndb-1 down",
		"too long":       ok + strings.Repeat("x", 300),
		"missing item":   "2 hosts, 14 containers, 13 running. all fine",
		"missing number": "2 hosts, 14 containers. db-1 down",
		"prefix number":  "22 hosts, 14 containers, 13 running. db-1 down",
		"control":        "2 hosts, 14 containers, 13 running.\x07 db-1 down",
		"carriage":       "2 hosts, 14 containers, 13 running.\r db-1 down",
		"bidi":           "2 hosts, 14 containers, 13 running. db-1 down‮",
	}
	for name, in := range cases {
		err := validateBriefing(in, facts, rule)
		if !errors.Is(err, ErrBadResponse) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	three := "2 hosts, 14 containers\n13 running\ndb-1 down"
	if err := validateBriefing(three, facts, rule); err != nil {
		t.Fatalf("three lines: %v", err)
	}
	if err := validateBriefing(strings.Repeat("é", 300), BriefingFacts{}, ""); err == nil {
		t.Fatal("zero facts need the digit 0")
	}
}
