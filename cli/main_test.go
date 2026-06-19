package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// run must not error on the no-op paths exercised at this scaffold stage.
func TestRun(t *testing.T) {
	t.Run("no args prints usage", func(t *testing.T) {
		require.NoError(t, run(nil))
	})
	t.Run("version flag", func(t *testing.T) {
		require.NoError(t, run([]string{"--version"}))
	})
	t.Run("unknown flag errors", func(t *testing.T) {
		require.Error(t, run([]string{"--nope"}))
	})
}
