package core

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestTemporaryFileExcludesOfficeSidecars(t *testing.T) {
	for _, name := range []string{".~lock.xlsx", ".~【预开发4.23上】king.xlsx", "~$book.xlsx", "~WRL0001.tmp"} {
		if !temporaryFile(name) {
			t.Fatalf("temporaryFile(%q) = false", name)
		}
	}
	if temporaryFile("正式配表.xlsx") {
		t.Fatal("正式文件不得被当成临时文件")
	}
}

func TestDiscoverSourceFollowsOnlyConfiguredRootLink(t *testing.T) {
	root := t.TempDir()
	realRoot := filepath.Join(root, "real")
	linkRoot := filepath.Join(root, "linked")
	if err := os.MkdirAll(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realRoot, "plan.md"), []byte("# plan"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("host cannot create directory symlink: %v", err)
	}
	source, err := CreateSourceConfig("linked", "Linked", "design", linkRoot, true)
	if err != nil {
		t.Fatal(err)
	}
	source.IndexIdentity = SourceIndexIdentity(source)
	result := discoverSource(context.Background(), source)
	if result.Err != nil || len(result.Candidates) != 1 {
		t.Fatalf("discovery=%#v", result)
	}
	candidate := result.Candidates[0]
	if CanonicalPathKey(candidate.AbsolutePath) != CanonicalPathKey(filepath.Join(linkRoot, "plan.md")) {
		t.Fatalf("candidate paths=%#v", candidate)
	}
	expectedInfo, err := os.Stat(filepath.Join(realRoot, "plan.md"))
	if err != nil {
		t.Fatal(err)
	}
	actualInfo, err := os.Stat(candidate.ReadPath)
	if err != nil || !os.SameFile(expectedInfo, actualInfo) {
		t.Fatalf("candidate read path does not preserve file identity: candidate=%#v err=%v", candidate, err)
	}
}
