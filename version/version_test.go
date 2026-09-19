package version

import (
	"strings"
	"testing"
)

func TestVersionInfo(t *testing.T) {
	if Version == "" {
		t.Fatal("Version should not be empty")
	}

	full := Full()
	if !strings.Contains(full, "connperf version") {
		t.Errorf("Full() expected to contain 'connperf version', got: %s", full)
	}
	if !strings.Contains(full, Version) {
		t.Errorf("Full() expected to contain Version %s, got: %s", Version, full)
	}

	short := Short()
	if !strings.Contains(short, "connperf") || !strings.Contains(short, Version) {
		t.Errorf("Short() expected to contain 'connperf' and %s, got: %s", Version, short)
	}
}
