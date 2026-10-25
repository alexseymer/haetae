package deployspec

import (
	"strings"
	"testing"
)

func check(t *testing.T, name string, f func(string) error, good, bad []string) {
	t.Helper()
	for _, s := range good {
		if err := f(s); err != nil {
			t.Errorf("%s(%q) rejected: %v", name, s, err)
		}
	}
	for _, s := range bad {
		if err := f(s); err == nil {
			t.Errorf("%s(%q) accepted", name, s)
		}
	}
}

func TestProjectName(t *testing.T) {
	check(t, "ProjectName", ProjectName,
		[]string{"shop", "a", "my-app-2", strings.Repeat("a", 40)},
		[]string{"", "Shop", "-shop", "shop_", "sh op", "a/b", "../x", "shop;rm", "$(id)", strings.Repeat("a", 41), "é"})
}

func TestDomain(t *testing.T) {
	check(t, "Domain", Domain,
		[]string{"", "shop.example.com", "a.b.co", "xn--bcher-kva.example", "app.localtest.me"},
		[]string{"localhost", "Shop.example.com", "https://x.com", "x.com:443", "x.com/path", "*.example.com", "1.2.3.4", "a..b.com",
			"-a.example.com", "a-.example.com", "x.com\nreverse_proxy evil:80", "x.com {", "x.com }", strings.Repeat("a", 64) + ".com"})
}

func TestService(t *testing.T) {
	check(t, "Service", Service, []string{"web", "api_2", "db.main", "Web-1"}, []string{"", "-x", "a b", "a/b", "$x", "a;b", strings.Repeat("a", 64)})
}

func TestPort(t *testing.T) {
	for p, ok := range map[uint32]bool{0: false, 1: true, 8080: true, 65535: true, 65536: false} {
		if (Port(p) == nil) != ok {
			t.Errorf("Port(%d) ok=%v", p, !ok)
		}
	}
}

func TestGitURL(t *testing.T) {
	check(t, "GitURL", func(s string) error { return GitURL(s, false) },
		[]string{"https://github.com/acme/shop", "https://github.com/acme/shop.git", "https://git.example.com:8443/a/b"},
		[]string{"", "http://github.com/a/b", "git@github.com:a/b.git", "ssh://git@github.com/a/b", "file:///tmp/repo", "https://user:pw@github.com/a/b",
			"https://github.com/a/b?x=1", "https://github.com/a/b#frag", "https://-evil.com/a", "https://github.com/../etc", "https://github.com/a b",
			"https://github.com/a;rm", "--upload-pack=evil", "https://github.com/a\nb", "ext::sh -c id", "https://github.com/`id`"})
	// Local file URLs only when explicitly allowed, and only absolute without "..".
	if err := GitURL("file:///tmp/repo.git", true); err != nil {
		t.Errorf("local url rejected when allowed: %v", err)
	}
	for _, bad := range []string{"file://host/tmp/x", "file:///tmp/../etc", "file://relative", "http://x/y"} {
		if err := GitURL(bad, true); err == nil {
			t.Errorf("GitURL(%q, allowLocal) accepted", bad)
		}
	}
}

func TestGitRef(t *testing.T) {
	check(t, "GitRef", GitRef,
		[]string{"main", "release/1.2", "feature/x_y-z", "v1.0.0", strings.Repeat("a", 40), "0123456789abcdef0123456789abcdef01234567"},
		[]string{"", "-main", "--upload-pack=x", "a..b", "a//b", "a/", "a.", "a.lock", "a@{1}", "a b", "a;b", "$(id)", "main\nx", "~1", "^HEAD", "a:b", "a?b", "a*b"})
	if !IsCommit(strings.Repeat("0", 40)) || IsCommit("main") || IsCommit(strings.Repeat("A", 40)) {
		t.Error("IsCommit")
	}
}

func TestComposePath(t *testing.T) {
	for in, want := range map[string]string{"": "compose.yaml", "compose.yml": "compose.yml", "deploy/prod/compose.yaml": "deploy/prod/compose.yaml"} {
		if got, err := ComposePath(in); err != nil || got != want {
			t.Errorf("ComposePath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"/etc/passwd", "../compose.yaml", "a/../../x.yaml", "a/./b.yaml", "a//b.yaml", "compose.txt", "-f.yaml", "a\\b.yaml", "..", "compose.yaml\x00", strings.Repeat("a", 130) + ".yaml"} {
		if _, err := ComposePath(bad); err == nil {
			t.Errorf("ComposePath(%q) accepted", bad)
		}
	}
}

func TestInlineCompose(t *testing.T) {
	for _, bad := range []string{"", "   \n", "services:\x00", "\xff\xfe", strings.Repeat("a", MaxInlineCompose+1)} {
		if InlineCompose(bad) == nil {
			t.Errorf("InlineCompose accepted %.20q", bad)
		}
	}
	if err := InlineCompose("services:\n  web:\n    image: nginx\n"); err != nil {
		t.Error(err)
	}
}
