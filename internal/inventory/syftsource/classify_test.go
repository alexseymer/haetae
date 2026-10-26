package syftsource

import (
	"context"
	"errors"
	"testing"
)

func TestClassifySourceErr(t *testing.T) {
	if !errors.Is(classifySourceErr(errors.New(`no source providers were able to resolve the input "x"`)), ErrUnsupported) {
		t.Fatal("no providers should be unsupported")
	}
	for _, e := range []error{errors.New("docker: cannot connect to the Docker daemon"), context.DeadlineExceeded} {
		if errors.Is(classifySourceErr(e), ErrUnsupported) {
			t.Fatalf("%v must be a generic failure", e)
		}
	}
}

func TestCheckOSPackages(t *testing.T) {
	deb := []Pkg{{Name: "a", Type: "deb"}}
	cases := []struct {
		name string
		r    Result
		fail bool
	}{
		{"distroless", Result{}, false},
		{"debian empty", Result{DistroName: "debian"}, true},
		{"debian only npm", Result{DistroName: "debian", Packages: []Pkg{{Type: "npm"}}}, true},
		{"debian ok", Result{DistroName: "debian", Packages: deb}, false},
		{"ubuntu like", Result{DistroName: "mint", DistroLike: "ubuntu debian"}, true},
		{"unknown distro", Result{DistroName: "weird"}, false},
	}
	for _, c := range cases {
		if err := checkOSPackages(&c.r); (err != nil) != c.fail {
			t.Errorf("%s: err=%v fail=%v", c.name, err, c.fail)
		}
	}
}
