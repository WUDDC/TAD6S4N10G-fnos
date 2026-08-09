package powerguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectProfile(t *testing.T) {
	tests := []struct {
		model string
		want  string
	}{
		{"Intel(R) Core(TM) i3-N305", "n305"},
		{"Intel(R) Processor N100", "n100"},
		{"Intel N100 CPU @ 0.80GHz", "n100"},
	}
	for _, test := range tests {
		profile, err := DetectProfile(test.model)
		if err != nil {
			t.Fatalf("DetectProfile(%q): %v", test.model, err)
		}
		if profile.ID != test.want {
			t.Fatalf("DetectProfile(%q)=%q, want %q", test.model, profile.ID, test.want)
		}
	}
	if _, err := DetectProfile("Intel Core i5-12500"); err == nil {
		t.Fatal("unsupported model was accepted")
	}
}

func TestApplyPackageAndVerify(t *testing.T) {
	dir := t.TempDir()
	longPath := filepath.Join(dir, "long")
	shortPath := filepath.Join(dir, "short")
	for _, path := range []string{longPath, shortPath} {
		if err := os.WriteFile(path, []byte("25000000"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pkg := Package{
		Name:  "package-0",
		Long:  &Constraint{PowerPath: longPath, CurrentUW: 25_000_000},
		Short: &Constraint{PowerPath: shortPath, CurrentUW: 35_000_000},
	}
	if err := applyPackage(pkg, 15_000_000, 25_000_000); err != nil {
		t.Fatal(err)
	}
	if got, _ := readInt(longPath); got != 15_000_000 {
		t.Fatalf("PL1=%d", got)
	}
	if got, _ := readInt(shortPath); got != 25_000_000 {
		t.Fatalf("PL2=%d", got)
	}
}

func TestWriteJSONAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	cfg := Config{Enabled: true, PL1W: 15, PL2W: 25, ReapplySeconds: 30}
	if err := writeJSONAtomic(path, cfg, 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatal("JSON file is empty or lacks final newline")
	}
}
