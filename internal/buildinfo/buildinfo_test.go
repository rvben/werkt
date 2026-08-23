package buildinfo

import "testing"

func TestCurrentAlwaysHasAnIdentity(t *testing.T) {
	value := Current()
	if value.Version == "" {
		t.Fatal("Version is empty")
	}
	if value.Commit == "" {
		t.Fatal("Commit is empty")
	}
}
