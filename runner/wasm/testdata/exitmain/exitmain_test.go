package exitmain

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) { os.Exit(3) }

func TestNothing(t *testing.T) {}
