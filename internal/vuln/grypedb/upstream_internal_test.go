package grypedb

import (
	"reflect"
	"testing"

	grypePkg "github.com/anchore/grype/grype/pkg"
)

func TestUpstreamsFromPURL(t *testing.T) {
	cases := []struct {
		name, pkg, purl string
		want            []grypePkg.UpstreamPackage
	}{
		{"deb name only", "libpam-modules", "pkg:deb/debian/libpam-modules@1.5.2-6?upstream=pam", []grypePkg.UpstreamPackage{{Name: "pam"}}},
		{"deb name and version", "libssl3", "pkg:deb/debian/libssl3@3.0.15-1?upstream=openssl%403.0.15-1", []grypePkg.UpstreamPackage{{Name: "openssl", Version: "3.0.15-1"}}},
		{"same as binary", "bash", "pkg:deb/debian/bash@5.2?upstream=bash", nil},
		{"apk origin", "libcrypto3", "pkg:apk/alpine/libcrypto3@3.3.2-r0?upstream=openssl", []grypePkg.UpstreamPackage{{Name: "openssl"}}},
		{"rpm source rpm", "openssl-libs", "pkg:rpm/redhat/openssl-libs@3.0.7-27.el9?upstream=openssl-3.0.7-27.el9.src.rpm", []grypePkg.UpstreamPackage{{Name: "openssl", Version: "3.0.7-27.el9"}}},
		{"rpm malformed", "x", "pkg:rpm/redhat/x@1?upstream=garbage", nil},
		{"no qualifier", "zlib1g", "pkg:deb/debian/zlib1g@1.2.13", nil},
		{"no purl", "zlib1g", "", nil},
		{"bad purl", "zlib1g", "not a purl", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := upstreamsFromPURL(c.pkg, c.purl); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %#v, want %#v", got, c.want)
			}
		})
	}
}
