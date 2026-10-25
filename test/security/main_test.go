//go:build e2e_security

package security_test

import (
	"os"
	"testing"

	"github.com/alexseymer/haetae/internal/testutil/dockerclean"
)

func TestMain(m *testing.M) {
	os.Exit(dockerclean.Run(m, "htest-"))
}
