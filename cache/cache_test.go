package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNameFollowsTheConvention(t *testing.T) {
	cases := []struct {
		id, source, label, want string
	}{
		{"55576444578", "https://live.staticflickr.com/65535/55576444578_1d148ca9ac_m.jpg", "Small", "55576444578_1d148ca9ac_small.jpg"},
		{"55576444578", "https://live.staticflickr.com/65535/55576444578_1d148ca9ac_k.jpg", "Large 2048", "55576444578_1d148ca9ac_large-2048.jpg"},
		{"55576444578", "https://live.staticflickr.com/65535/55576444578_9f8e7d6c5b_o.png", "Original", "55576444578_9f8e7d6c5b_original.png"},
		{"55576444578", "https://live.staticflickr.com/65535/55576444578_1d148ca9ac.jpg", "Medium", "55576444578_1d148ca9ac_medium.jpg"},
	}
	for _, c := range cases {
		got, err := Name(c.id, c.source, c.label)
		if err != nil || got != c.want {
			t.Errorf("Name(%s, %s) = %q, %v; want %q", c.label, c.source, got, err, c.want)
		}
		if !Matches(got) {
			t.Errorf("%s does not match the pattern it was built from", got)
		}
	}
	for _, bad := range []struct{ id, source string }{
		{"1", "https://www.flickr.com/photos/x/1/play/site/abc/"},       // a video page, not a file
		{"2", "https://live.staticflickr.com/65535/1_1d148ca9ac_m.jpg"}, // another photo
		{"1", "https://live.staticflickr.com/65535/1_NOTHEX_m.jpg"},     // not a secret
		{"1", "::"}, // not a URL
		{"../1", "https://live.staticflickr.com/65535/../1_1d1_m.jpg"}, // not an id
	} {
		if name, err := Name(bad.id, bad.source, "Large"); err == nil {
			t.Errorf("Name(%s, %s) = %q, want an error", bad.id, bad.source, name)
		}
	}
}

func TestMatchesOnlyCacheFiles(t *testing.T) {
	for name, want := range map[string]bool{
		"1_abc_large.jpg":       true,
		"1_abc_large-2048.jpeg": true,
		"notes.txt":             false,
		"1_abc_large.jpg.tmp":   false,
		"x_abc_large.jpg":       false,
		"1_ABC_large.jpg":       false,
	} {
		if Matches(name) != want {
			t.Errorf("Matches(%q) = %v", name, !want)
		}
	}
}

func write(t *testing.T, dir, name string, size int, age time.Duration) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	if err := os.Chtimes(p, when, when); err != nil {
		t.Fatal(err)
	}
}

func TestScanCountsCacheFilesOldestFirst(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "1_aa_large.jpg", 100, time.Hour)
	write(t, dir, "2_bb_large.jpg", 200, 72*time.Hour)
	write(t, dir, "3_cc_small.jpg", 50, 24*time.Hour)
	write(t, dir, "mine.jpg", 1000, 0) // not ours
	if err := os.Mkdir(filepath.Join(dir, "4_dd_large.jpg"), 0o700); err != nil {
		t.Fatal(err)
	}
	s, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Bytes != 350 || s.Files != 3 {
		t.Fatalf("bytes=%d files=%d", s.Bytes, s.Files)
	}
	if s.Entries[0].Name != "2_bb_large.jpg" || s.Entries[2].Name != "1_aa_large.jpg" {
		t.Fatalf("not oldest first: %+v", s.Entries)
	}
	if _, err := Scan(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("a missing directory must fail")
	}
}

func TestPlanPurge(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "1_aa_large.jpg", 100, time.Hour)
	write(t, dir, "2_bb_large.jpg", 200, 72*time.Hour)
	write(t, dir, "3_cc_small.jpg", 50, 24*time.Hour)
	s, _ := Scan(dir)

	// Down to 100 bytes: the two oldest go (200 + 50), leaving 100.
	got := PlanToSize(s, 100)
	if len(got) != 2 || got[0].Name != "2_bb_large.jpg" || got[1].Name != "3_cc_small.jpg" {
		t.Fatalf("to size: %+v", got)
	}
	if len(PlanToSize(s, 1000)) != 0 {
		t.Fatal("nothing to remove when already small enough")
	}
	got = PlanOlderThan(s, time.Now().Add(-12*time.Hour))
	if len(got) != 2 {
		t.Fatalf("older than 12h: %+v", got)
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "1_aa_large.jpg", 10, time.Hour)
	s, _ := Scan(dir)
	freed, err := Remove(s.Entries)
	if err != nil || freed != 10 {
		t.Fatalf("freed %d, %v", freed, err)
	}
	if _, err := Remove(s.Entries); err == nil {
		t.Fatal("removing a file twice must fail")
	}
}

func TestTouchMakesAFileTheNewest(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "1_aa_large.jpg", 10, 72*time.Hour)
	write(t, dir, "2_bb_large.jpg", 10, time.Hour)
	if err := Touch(filepath.Join(dir, "1_aa_large.jpg")); err != nil {
		t.Fatal(err)
	}
	s, _ := Scan(dir)
	if s.Entries[1].Name != "1_aa_large.jpg" {
		t.Fatal("a cache hit must count as a recent use")
	}
}

func TestHuman(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 999: "999 B", 1536: "1.5 KiB", 5 << 30: "5.0 GiB", 3 << 20: "3.0 MiB"} {
		if got := Human(n); got != want {
			t.Errorf("Human(%d) = %q, want %q", n, got, want)
		}
	}
}
