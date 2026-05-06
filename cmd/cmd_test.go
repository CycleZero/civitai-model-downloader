package cmd

import (
	"testing"
)

func TestDownloadCommandArgs(t *testing.T) {
	rootCmd.SetArgs([]string{"download", "--help"})
	if err := rootCmd.Execute(); err != nil {
		t.Error(err)
	}
}
