//go:build deploy

package ingress

import (
	"os"
	"testing"

	"github.com/alexseymer/haetae/internal/testutil/dockerclean"
)

func TestMain(m *testing.M) {
	os.Exit(dockerclean.Run(m, "haetae-ing-", "haetae-caddy-test"))
}
