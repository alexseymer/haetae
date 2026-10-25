//go:build deploy

package deploy

import (
	"os"
	"testing"

	"github.com/alexseymer/haetae/internal/testutil/dockerclean"
)

func TestMain(m *testing.M) {
	os.Exit(dockerclean.Run(m, "htest-"))
}
