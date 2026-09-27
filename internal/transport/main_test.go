package transport

import (
	"os"
	"testing"

	"pi-bridge-go/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(testutil.Run(m))
}
