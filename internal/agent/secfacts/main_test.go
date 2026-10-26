//go:build deploy

package secfacts_test

import (
	"os"
	"testing"

	"github.com/alexseymer/haetae/internal/testutil/dockerclean"
)

func TestMain(m *testing.M) {
	os.Exit(dockerclean.Run(m, "htest-"))
}
