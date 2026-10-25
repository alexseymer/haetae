//go:build e2e_deploy

package deploy_test

import (
	"os"
	"testing"

	"github.com/alexseymer/haetae/internal/testutil/dockerclean"
)

func TestMain(m *testing.M) {
	os.Exit(dockerclean.Run(m, "htest-"))
}
