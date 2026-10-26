package agent

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/alexseymer/haetae/internal/agent/deploy"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

func commandKinds(t *testing.T) []string {
	t.Helper()
	od := (&agentv1.Command{}).ProtoReflect().Descriptor().Oneofs().ByName("kind")
	if od == nil {
		t.Fatal("Command has no oneof kind")
	}
	var kinds []string
	for i := range od.Fields().Len() {
		kinds = append(kinds, string(od.Fields().Get(i).Name()))
	}
	return kinds
}

// Adding a Command kind without classifying it fails here.
func TestEveryCommandKindIsClassified(t *testing.T) {
	kinds := commandKinds(t)
	if len(kinds) < 8 {
		t.Fatalf("only %d kinds enumerated: %v", len(kinds), kinds)
	}
	for _, k := range kinds {
		if _, ok := commandClasses[k]; !ok {
			t.Errorf("command kind %q has no entry in commandClasses", k)
		}
	}
	for k := range commandClasses {
		if !slices.Contains(kinds, k) {
			t.Errorf("commandClasses has %q, which is not a Command kind", k)
		}
	}
}

func TestCommandClassesAreUserOrOperator(t *testing.T) {
	var operator []string
	for k, c := range commandClasses {
		switch c {
		case ClassUser:
		case ClassOperator:
			operator = append(operator, k)
		default:
			t.Errorf("kind %q has class %q", k, c)
		}
	}
	slices.Sort(operator)
	if !slices.Equal(operator, []string{"restart_container"}) {
		t.Fatalf("operator-class kinds = %v, want exactly [restart_container]", operator)
	}
}

// No kind or class deletes data. remove_project is the one existing
// user-class kind with "remove" in its name; it keeps volumes (next test).
func TestNoDataDeletingKinds(t *testing.T) {
	bad := []string{"delete", "remove", "prune", "rm", "purge", "wipe", "destroy", "drop"}
	for k, c := range commandClasses {
		if k == "remove_project" {
			if c != ClassUser {
				t.Errorf("remove_project must be user class, is %q", c)
			}
			continue
		}
		for _, w := range bad {
			for _, part := range strings.Split(k+"_"+c, "_") {
				if part == w {
					t.Errorf("kind %q / class %q looks like data deletion (%q)", k, c, w)
				}
			}
		}
	}
	for _, k := range commandKinds(t) {
		if strings.Contains(k, "volume") {
			t.Errorf("kind %q touches volumes", k)
		}
	}
}

// remove_project runs "docker compose down --remove-orphans" and never
// passes a volume-removal flag (-v / --volumes), so volumes are kept.
func TestRemoveProjectKeepsVolumes(t *testing.T) {
	runner := &deploy.FakeRunner{}
	runner.Add(deploy.FakeStep{Match: func(deploy.Cmd) bool { return true }, Repeat: -1})
	m := deploy.New(deploy.Config{StateDir: t.TempDir(), Runner: runner})
	defer m.Close()
	res := dispatch(context.Background(), &agentv1.Command{
		Id:   "rm1",
		Kind: &agentv1.Command_RemoveProject{RemoveProject: &agentv1.RemoveProject{Project: "shop"}},
	}, RunOptions{Deploys: m, Policy: NewPolicy("", nil)})
	if res.GetError() != "" {
		t.Fatalf("remove failed: %s", res.GetError())
	}
	if len(runner.Calls) == 0 {
		t.Fatal("no docker invocation recorded")
	}
	for _, c := range runner.Calls {
		for _, a := range c.Args {
			if a == "-v" || a == "--volumes" || strings.Contains(a, "volume") || a == "prune" || a == "rm" {
				t.Errorf("remove passed %q in %v", a, c.Args)
			}
		}
	}
}
