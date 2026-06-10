package gitjobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// setupLFSRepo creates a git repository with .gitattributes configured for LFS tracking.
// It commits the .gitattributes file and returns the repo path and *git.Repository.
func setupLFSRepo(t *testing.T) (string, *git.Repository) {
	t.Helper()
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".gitattributes"), "*.bin filter=lfs diff=lfs merge=lfs -text\n")
	writeFile(t, filepath.Join(root, ".lfsconfig"), "[lfs]\n\turl = http://localhost:0/info/lfs\n")
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add(".gitattributes"); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add(".lfsconfig"); err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()}
	if _, err := worktree.Commit("init: add .gitattributes for LFS", &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatal(err)
	}
	return root, repo
}

// addAndCommitLFSFile adds a file to the worktree, stages it with LFS cleaning, and commits.
// Returns the lfsPointer for the file content.
func addAndCommitLFSFile(t *testing.T, repoPath string, repo *git.Repository, name, content string) lfsPointer {
	t.Helper()
	writeFile(t, filepath.Join(repoPath, name), content)

	// Stage the file and clean LFS index
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add(name); err != nil {
		t.Fatal(err)
	}
	cleaned, err := cleanLFSIndex(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range cleaned {
		if p == name {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected %q to be LFS-cleaned, got cleaned=%v", name, cleaned)
	}

	sig := &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()}
	if _, err := worktree.Commit("add "+name, &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatal(err)
	}

	pointer, err := cacheLFSFile(repoPath, name)
	if err != nil {
		t.Fatal(err)
	}
	return pointer
}

// --------------------------------------------------------------------------
// Tests for low-level LFS helpers
// --------------------------------------------------------------------------

func TestRealRepo_LFSObjectPath(t *testing.T) {
	oid := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	p := lfsObjectPath("/myrepo", oid)
	expected := filepath.Join("/myrepo", ".git", "lfs", "objects", "ab", "cd", oid)
	if p != expected {
		t.Fatalf("lfsObjectPath = %q, want %q", p, expected)
	}
}

func TestRealRepo_HashFile(t *testing.T) {
	root := t.TempDir()
	content := "hello LFS world"
	fp := filepath.Join(root, "data.bin")
	writeFile(t, fp, content)

	pointer, err := hashFile(fp)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(content))
	wantOID := hex.EncodeToString(hash[:])
	if pointer.OID != wantOID {
		t.Fatalf("OID = %q, want %q", pointer.OID, wantOID)
	}
	if pointer.Size != int64(len(content)) {
		t.Fatalf("Size = %d, want %d", pointer.Size, len(content))
	}
}

func TestRealRepo_HashFile_Empty(t *testing.T) {
	root := t.TempDir()
	fp := filepath.Join(root, "empty.bin")
	writeFile(t, fp, "")

	pointer, err := hashFile(fp)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte{})
	wantOID := hex.EncodeToString(hash[:])
	if pointer.OID != wantOID {
		t.Fatalf("OID = %q, want %q", pointer.OID, wantOID)
	}
	if pointer.Size != 0 {
		t.Fatalf("Size = %d, want 0", pointer.Size)
	}
}

func TestRealRepo_HashFile_Nonexistent(t *testing.T) {
	_, err := hashFile("/nonexistent/path/file.bin")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestRealRepo_EncodeDecodePointer_EdgeCases(t *testing.T) {
	// Zero-size pointer
	p := lfsPointer{OID: strings.Repeat("0", 64), Size: 0}
	encoded := encodeLFSPointer(p)
	decoded, ok := decodeLFSPointer(encoded)
	if !ok || decoded != p {
		t.Fatalf("zero-size round trip failed: %#v", decoded)
	}

	// Large size
	p = lfsPointer{OID: strings.Repeat("a", 64), Size: 1<<40 - 1}
	encoded = encodeLFSPointer(p)
	decoded, ok = decodeLFSPointer(encoded)
	if !ok || decoded != p {
		t.Fatalf("large-size round trip failed: got %#v, want %#v", decoded, p)
	}

	// Various invalid pointers
	for _, inv := range [][]byte{
		nil,
		{},
		make([]byte, 1024), // too large
		[]byte("version wrong\noid sha256:" + strings.Repeat("a", 64) + "\nsize 10\n"),
		[]byte("version https://git-lfs.github.com/spec/v1\noid sha256:tooshort\nsize 10\n"),
		[]byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("g", 64) + "\nsize 10\n"), // non-hex
		[]byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("a", 64) + "\nsize NaN\n"),
		[]byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + strings.Repeat("a", 64) + "\n"), // missing size
	} {
		if _, ok := decodeLFSPointer(inv); ok {
			t.Fatalf("unexpected valid pointer for input: %q", inv)
		}
	}
}

func TestRealRepo_UniquePointers(t *testing.T) {
	p1 := lfsPointer{OID: "aaa", Size: 10}
	p2 := lfsPointer{OID: "bbb", Size: 20}
	p3 := lfsPointer{OID: "aaa", Size: 10} // duplicate of p1

	input := map[string]lfsPointer{
		"file1.bin": p1,
		"file2.bin": p2,
		"file3.bin": p3,
	}
	result := uniqueLFSPointers(input)
	if len(result) != 2 {
		t.Fatalf("expected 2 unique pointers, got %d", len(result))
	}
	if _, ok := result["aaa"]; !ok {
		t.Fatal("expected OID 'aaa' in result")
	}
	if _, ok := result["bbb"]; !ok {
		t.Fatal("expected OID 'bbb' in result")
	}
}

// --------------------------------------------------------------------------
// Tests for cacheLFSFile
// --------------------------------------------------------------------------

func TestRealRepo_CacheFile_Basic(t *testing.T) {
	root := t.TempDir()
	_, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	content := "binary data for LFS cache"
	writeFile(t, filepath.Join(root, "asset.bin"), content)

	pointer, err := cacheLFSFile(root, "asset.bin")
	if err != nil {
		t.Fatal(err)
	}

	hash := sha256.Sum256([]byte(content))
	wantOID := hex.EncodeToString(hash[:])
	if pointer.OID != wantOID {
		t.Fatalf("OID = %q, want %q", pointer.OID, wantOID)
	}
	if pointer.Size != int64(len(content)) {
		t.Fatalf("Size = %d, want %d", pointer.Size, len(content))
	}

	// Verify object stored on disk
	objPath := lfsObjectPath(root, pointer.OID)
	data, err := os.ReadFile(objPath)
	if err != nil {
		t.Fatalf("object not found at %s: %v", objPath, err)
	}
	if string(data) != content {
		t.Fatalf("stored content = %q, want %q", string(data), content)
	}
}

func TestRealRepo_CacheFile_Idempotent(t *testing.T) {
	root := t.TempDir()
	_, _ = git.PlainInit(root, false)
	content := "idempotent test content"
	writeFile(t, filepath.Join(root, "data.bin"), content)

	p1, err := cacheLFSFile(root, "data.bin")
	if err != nil {
		t.Fatal(err)
	}
	p2, err := cacheLFSFile(root, "data.bin")
	if err != nil {
		t.Fatal(err)
	}
	if p1 != p2 {
		t.Fatalf("idempotent cache returned different pointers: %+v vs %+v", p1, p2)
	}
}

func TestRealRepo_CacheFile_Nonexistent(t *testing.T) {
	root := t.TempDir()
	_, _ = git.PlainInit(root, false)
	_, err := cacheLFSFile(root, "nonexistent.bin")
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestRealRepo_CacheFile_LargeContent(t *testing.T) {
	root := t.TempDir()
	_, _ = git.PlainInit(root, false)
	// Create a file larger than typical pointer threshold
	content := strings.Repeat("ABCDEFGHIJ", 10000) // 100KB
	writeFile(t, filepath.Join(root, "large.bin"), content)

	pointer, err := cacheLFSFile(root, "large.bin")
	if err != nil {
		t.Fatal(err)
	}
	if pointer.Size != int64(len(content)) {
		t.Fatalf("Size = %d, want %d", pointer.Size, len(content))
	}

	objPath := lfsObjectPath(root, pointer.OID)
	stat, err := os.Stat(objPath)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Size() != int64(len(content)) {
		t.Fatalf("stored file size = %d, want %d", stat.Size(), len(content))
	}
}

// --------------------------------------------------------------------------
// Tests for LFS matcher and tracking
// --------------------------------------------------------------------------

func TestRealRepo_LFSMatcher_WithAttributes(t *testing.T) {
	root, _ := setupLFSRepo(t)
	matcher, enabled, err := lfsMatcher(root)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("expected LFS matcher to be enabled")
	}
	if matcher == nil {
		t.Fatal("expected non-nil matcher")
	}
	if !isLFSTracked(matcher, "test.bin") {
		t.Fatal("expected *.bin to be LFS-tracked")
	}
	if !isLFSTracked(matcher, "subdir/nested.bin") {
		t.Fatal("expected subdir/*.bin to be LFS-tracked via glob")
	}
	if isLFSTracked(matcher, "readme.txt") {
		t.Fatal("expected *.txt to NOT be LFS-tracked")
	}
	if isLFSTracked(matcher, "main.wa") {
		t.Fatal("expected *.wa to NOT be LFS-tracked")
	}
	if !isLFSTracked(matcher, "test.BIN") {
		t.Fatal("expected *.BIN to be LFS-tracked (case-insensitive)")
	}
	if !isLFSTracked(matcher, "subdir/PHOTO.Bin") {
		t.Fatal("expected subdir/*.Bin to be LFS-tracked (case-insensitive)")
	}
}

func TestRealRepo_LFSMatcher_NoAttributes(t *testing.T) {
	root := t.TempDir()
	_, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	matcher, enabled, err := lfsMatcher(root)
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("expected LFS matcher to be disabled without .gitattributes")
	}
	if matcher != nil {
		t.Fatal("expected nil matcher when disabled")
	}
}

func TestRealRepo_LFSMatcher_MultiplePatterns(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".gitattributes"),
		"*.bin filter=lfs diff=lfs merge=lfs -text\n"+
			"*.dat filter=lfs diff=lfs merge=lfs -text\n"+
			"*.txt text\n")
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add(".gitattributes"); err != nil {
		t.Fatal(err)
	}

	matcher, enabled, err := lfsMatcher(root)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("expected LFS matcher to be enabled")
	}

	for _, tracked := range []string{"file.bin", "data.dat"} {
		if !isLFSTracked(matcher, tracked) {
			t.Fatalf("expected %s to be LFS-tracked", tracked)
		}
	}
	if isLFSTracked(matcher, "notes.txt") {
		t.Fatal("expected notes.txt to NOT be LFS-tracked (text attribute, not lfs)")
	}
}

// --------------------------------------------------------------------------
// Tests for cleanLFSIndex
// --------------------------------------------------------------------------

func TestRealRepo_CleanIndex(t *testing.T) {
	root, repo := setupLFSRepo(t)
	content := strings.Repeat("LFS indexed content\n", 512)
	writeFile(t, filepath.Join(root, "data.bin"), content)

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("data.bin"); err != nil {
		t.Fatal(err)
	}

	cleaned, err := cleanLFSIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range cleaned {
		if p == "data.bin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected data.bin to be cleaned, got %v", cleaned)
	}

	// Verify index now contains an LFS pointer
	indexData, err := readIndexFile(repo, "data.bin")
	if err != nil {
		t.Fatal(err)
	}
	pointer, ok := decodeLFSPointer(indexData)
	if !ok {
		t.Fatalf("expected index to contain LFS pointer, got %q", indexData)
	}

	hash := sha256.Sum256([]byte(content))
	wantOID := hex.EncodeToString(hash[:])
	if pointer.OID != wantOID {
		t.Fatalf("pointer OID = %q, want %q", pointer.OID, wantOID)
	}

	// Verify the worktree file still has the original content
	actual, err := os.ReadFile(filepath.Join(root, "data.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(actual) != content {
		t.Fatal("worktree file should still contain original content after clean")
	}
}

func TestRealRepo_CleanIndex_SkipsAlreadyPointer(t *testing.T) {
	root, repo := setupLFSRepo(t)
	content := "already a pointer test"
	writeFile(t, filepath.Join(root, "data.bin"), content)

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("data.bin"); err != nil {
		t.Fatal(err)
	}

	// First clean converts to pointer
	_, err = cleanLFSIndex(root)
	if err != nil {
		t.Fatal(err)
	}

	// Second clean should return empty (already a pointer)
	cleaned2, err := cleanLFSIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range cleaned2 {
		if p == "data.bin" {
			t.Fatal("expected data.bin to be skipped on second clean (already a pointer)")
		}
	}
}

func TestRealRepo_CleanIndex_NonLFSTracked(t *testing.T) {
	root, repo := setupLFSRepo(t)
	writeFile(t, filepath.Join(root, "readme.txt"), "not tracked by LFS")

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add("readme.txt"); err != nil {
		t.Fatal(err)
	}

	cleaned, err := cleanLFSIndex(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range cleaned {
		if p == "readme.txt" {
			t.Fatal("expected non-LFS file to not be cleaned")
		}
	}
}

// --------------------------------------------------------------------------
// Tests for hydrateLFSFile
// --------------------------------------------------------------------------

func TestRealRepo_HydrateFile(t *testing.T) {
	root := t.TempDir()
	_, _ = git.PlainInit(root, false)
	content := "hydrated content"
	hash := sha256.Sum256([]byte(content))
	oid := hex.EncodeToString(hash[:])

	// Manually store the LFS object
	objPath := lfsObjectPath(root, oid)
	writeFile(t, objPath, content)

	pointer := lfsPointer{OID: oid, Size: int64(len(content))}
	if err := hydrateLFSFile(root, "output.bin", pointer); err != nil {
		t.Fatal(err)
	}

	result, err := os.ReadFile(filepath.Join(root, "output.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != content {
		t.Fatalf("hydrated content = %q, want %q", string(result), content)
	}
}

func TestRealRepo_HydrateFile_NestedPath(t *testing.T) {
	root := t.TempDir()
	_, _ = git.PlainInit(root, false)
	content := "nested hydration"
	hash := sha256.Sum256([]byte(content))
	oid := hex.EncodeToString(hash[:])

	objPath := lfsObjectPath(root, oid)
	writeFile(t, objPath, content)

	pointer := lfsPointer{OID: oid, Size: int64(len(content))}
	if err := hydrateLFSFile(root, "subdir/deep/output.bin", pointer); err != nil {
		t.Fatal(err)
	}

	result, err := os.ReadFile(filepath.Join(root, "subdir", "deep", "output.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(result) != content {
		t.Fatalf("hydrated content = %q, want %q", string(result), content)
	}
}

func TestRealRepo_HydrateFile_MissingObject(t *testing.T) {
	root := t.TempDir()
	_, _ = git.PlainInit(root, false)
	fakeOID := strings.Repeat("f", 64)
	pointer := lfsPointer{OID: fakeOID, Size: 100}

	err := hydrateLFSFile(root, "output.bin", pointer)
	if err == nil {
		t.Fatal("expected error when LFS object is missing")
	}
}

// --------------------------------------------------------------------------
// Tests for dehydrate/rehydrate cycle
// --------------------------------------------------------------------------

func TestRealRepo_DehydrateRehydrate(t *testing.T) {
	root, repo := setupLFSRepo(t)
	content := strings.Repeat("dehydrate me\n", 2048)
	pointer := addAndCommitLFSFile(t, root, repo, "asset.bin", content)

	// Verify worktree has real content
	data, err := os.ReadFile(filepath.Join(root, "asset.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Fatalf("worktree should have real content before dehydrate")
	}

	// Dehydrate: replace worktree files with pointers
	dehydrated, err := dehydrateCleanLFSFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(dehydrated) != 1 {
		t.Fatalf("expected 1 dehydrated file, got %d", len(dehydrated))
	}
	dehydratedPointer, ok := dehydrated["asset.bin"]
	if !ok {
		t.Fatal("expected asset.bin to be dehydrated")
	}
	if dehydratedPointer != pointer {
		t.Fatalf("dehydrated pointer mismatch: %+v vs %+v", dehydratedPointer, pointer)
	}

	// Verify worktree now has pointer data
	data, err = os.ReadFile(filepath.Join(root, "asset.bin"))
	if err != nil {
		t.Fatal(err)
	}
	decodedPointer, ok := decodeLFSPointer(data)
	if !ok {
		t.Fatalf("expected worktree to contain LFS pointer after dehydrate, got %q", data)
	}
	if decodedPointer.OID != pointer.OID {
		t.Fatalf("pointer OID mismatch after dehydrate")
	}

	// Rehydrate: restore real content
	if err := rehydrateLFSFiles(root, dehydrated); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(filepath.Join(root, "asset.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != content {
		t.Fatalf("expected original content after rehydrate, got %q", string(data))
	}
}

func TestRealRepo_Dehydrate_SkipsModifiedFiles(t *testing.T) {
	root, repo := setupLFSRepo(t)
	content := "original content"
	_ = addAndCommitLFSFile(t, root, repo, "asset.bin", content)

	// Modify the file so it no longer matches the index pointer
	writeFile(t, filepath.Join(root, "asset.bin"), "modified content")

	dehydrated, err := dehydrateCleanLFSFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := dehydrated["asset.bin"]; ok {
		t.Fatal("expected modified file to be skipped during dehydrate")
	}
}

// --------------------------------------------------------------------------
// Tests for validLFSObject
// --------------------------------------------------------------------------

func TestRealRepo_ValidObject(t *testing.T) {
	root := t.TempDir()
	_, _ = git.PlainInit(root, false)
	content := "valid object test"
	hash := sha256.Sum256([]byte(content))
	oid := hex.EncodeToString(hash[:])

	// Not valid before storing
	pointer := lfsPointer{OID: oid, Size: int64(len(content))}
	if validLFSObject(root, pointer) {
		t.Fatal("expected object to be invalid before storing")
	}

	// Store the object
	writeFile(t, lfsObjectPath(root, oid), content)

	// Now it should be valid
	if !validLFSObject(root, pointer) {
		t.Fatal("expected object to be valid after storing")
	}
}

func TestRealRepo_ValidObject_CorruptContent(t *testing.T) {
	root := t.TempDir()
	_, _ = git.PlainInit(root, false)
	content := "valid content"
	hash := sha256.Sum256([]byte(content))
	oid := hex.EncodeToString(hash[:])

	// Store CORRUPT content (wrong hash)
	writeFile(t, lfsObjectPath(root, oid), "corrupt content")

	pointer := lfsPointer{OID: oid, Size: int64(len(content))}
	if validLFSObject(root, pointer) {
		t.Fatal("expected corrupt object to be invalid")
	}
}

func TestRealRepo_ValidObject_WrongSize(t *testing.T) {
	root := t.TempDir()
	_, _ = git.PlainInit(root, false)
	content := "content with size"
	hash := sha256.Sum256([]byte(content))
	oid := hex.EncodeToString(hash[:])

	writeFile(t, lfsObjectPath(root, oid), content)

	// Wrong size
	pointer := lfsPointer{OID: oid, Size: 999}
	if validLFSObject(root, pointer) {
		t.Fatal("expected wrong-size object to be invalid")
	}
}

// --------------------------------------------------------------------------
// Tests for normalizeLFSStatus
// --------------------------------------------------------------------------

func TestRealRepo_NormalizeStatus(t *testing.T) {
	root, repo := setupLFSRepo(t)
	content := strings.Repeat("status test content\n", 256)
	_ = addAndCommitLFSFile(t, root, repo, "data.bin", content)

	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	status, err := worktree.Status()
	if err != nil {
		t.Fatal(err)
	}

	// Before normalization, the file might show as modified if worktree has real content
	// and index has a pointer. Normalize should fix this.
	if err := normalizeLFSStatus(root, status); err != nil {
		t.Fatal(err)
	}

	// After normalization, the file should be unmodified
	fileStatus, ok := status["data.bin"]
	if !ok {
		t.Fatal("expected data.bin in status")
	}
	if fileStatus.Worktree != git.Unmodified {
		t.Fatalf("expected data.bin worktree to be Unmodified after normalize, got %q", fileStatus.Worktree)
	}
}

// --------------------------------------------------------------------------
// Tests for LFS endpoint discovery
// --------------------------------------------------------------------------

func TestRealRepo_LFSEndpoint_FromConfig(t *testing.T) {
	_, repo := setupLFSRepo(t)
	cfg, err := repo.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Raw.Section("lfs").SetOption("url", "https://lfs.example.com/repo")
	if err := repo.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}

	endpoint, err := lfsEndpoint(repo, "origin")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://lfs.example.com/repo" {
		t.Fatalf("endpoint = %q, want %q", endpoint, "https://lfs.example.com/repo")
	}
}

func TestRealRepo_LFSEndpoint_FromLFSConfig(t *testing.T) {
	root, repo := setupLFSRepo(t)
	writeFile(t, filepath.Join(root, ".lfsconfig"), "[lfs]\n\turl = https://lfsconfig.example.com/repo\n")

	endpoint, err := lfsEndpoint(repo, "origin")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://lfsconfig.example.com/repo" {
		t.Fatalf("endpoint = %q, want %q", endpoint, "https://lfsconfig.example.com/repo")
	}
}

func TestRealRepo_LFSEndpoint_FromRemoteURL(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{"https://github.com/user/repo"},
	})
	if err != nil {
		t.Fatal(err)
	}

	endpoint, err := lfsEndpoint(repo, "origin")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://github.com/user/repo.git/info/lfs"
	if endpoint != want {
		t.Fatalf("endpoint = %q, want %q", endpoint, want)
	}
}

func TestRealRepo_LFSEndpoint_RemoteWithDotGit(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{"https://github.com/user/repo.git"},
	})
	if err != nil {
		t.Fatal(err)
	}

	endpoint, err := lfsEndpoint(repo, "origin")
	if err != nil {
		t.Fatal(err)
	}
	want := "https://github.com/user/repo.git/info/lfs"
	if endpoint != want {
		t.Fatalf("endpoint = %q, want %q", endpoint, want)
	}
}

func TestRealRepo_LFSEndpoint_NoRemote(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = lfsEndpoint(repo, "origin")
	if err == nil {
		t.Fatal("expected error when no remote is configured")
	}
}

func TestRealRepo_LFSEndpoint_NonHTTP(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{"git@github.com:user/repo.git"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = lfsEndpoint(repo, "origin")
	if err == nil {
		t.Fatal("expected error for non-HTTP remote")
	}
}

// --------------------------------------------------------------------------
// Tests for lfsPointersAtCommit / lfsPointersAtHead
// --------------------------------------------------------------------------

func TestRealRepo_PointersAtHead(t *testing.T) {
	root, repo := setupLFSRepo(t)
	content1 := strings.Repeat("pointer content 1\n", 100)
	content2 := strings.Repeat("pointer content 2\n", 200)
	_ = addAndCommitLFSFile(t, root, repo, "file1.bin", content1)
	_ = addAndCommitLFSFile(t, root, repo, "file2.bin", content2)

	pointers, err := lfsPointersAtHead(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(pointers) != 2 {
		t.Fatalf("expected 2 pointers at HEAD, got %d: %+v", len(pointers), pointers)
	}
	if _, ok := pointers["file1.bin"]; !ok {
		t.Fatal("expected file1.bin pointer")
	}
	if _, ok := pointers["file2.bin"]; !ok {
		t.Fatal("expected file2.bin pointer")
	}
}

func TestRealRepo_PointersAtCommit_SpecificCommit(t *testing.T) {
	root, repo := setupLFSRepo(t)
	content := strings.Repeat("specific commit content\n", 100)
	_ = addAndCommitLFSFile(t, root, repo, "file1.bin", content)

	// Get the commit hash
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}

	pointers, err := lfsPointersAtCommit(repo, head.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if len(pointers) != 1 {
		t.Fatalf("expected 1 pointer at commit, got %d", len(pointers))
	}
	if _, ok := pointers["file1.bin"]; !ok {
		t.Fatal("expected file1.bin pointer")
	}
}

func TestRealRepo_PointersAtHead_NoLFSFiles(t *testing.T) {
	_, repo := setupLFSRepo(t)
	// Only .gitattributes committed, no LFS files
	pointers, err := lfsPointersAtHead(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(pointers) != 0 {
		t.Fatalf("expected 0 pointers, got %d", len(pointers))
	}
}

func TestRealRepo_PointersReachable(t *testing.T) {
	root, repo := setupLFSRepo(t)

	content1 := strings.Repeat("v1 content\n", 100)
	p1 := addAndCommitLFSFile(t, root, repo, "data.bin", content1)
	head1, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}

	content2 := strings.Repeat("v2 content\n", 200)
	p2 := addAndCommitLFSFile(t, root, repo, "data.bin", content2)
	head2, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}

	// From HEAD (both commits reachable)
	pointers, err := lfsPointersReachable(repo, head2.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if len(pointers) != 2 {
		t.Fatalf("expected 2 reachable pointers, got %d", len(pointers))
	}
	if _, ok := pointers[p1.OID]; !ok {
		t.Fatalf("expected OID %s in reachable pointers", p1.OID)
	}
	if _, ok := pointers[p2.OID]; !ok {
		t.Fatalf("expected OID %s in reachable pointers", p2.OID)
	}

	// From first commit only
	pointers1, err := lfsPointersReachable(repo, head1.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if len(pointers1) != 1 {
		t.Fatalf("expected 1 reachable pointer from first commit, got %d", len(pointers1))
	}
	if _, ok := pointers1[p1.OID]; !ok {
		t.Fatalf("expected OID %s in reachable pointers from first commit", p1.OID)
	}
}

// --------------------------------------------------------------------------
// Tests for lfsPointersForFetch
// --------------------------------------------------------------------------

func TestRealRepo_PointersForFetch(t *testing.T) {
	root, repo := setupLFSRepo(t)
	content := strings.Repeat("fetch test content\n", 100)
	pointer := addAndCommitLFSFile(t, root, repo, "asset.bin", content)

	// Set up a fake remote ref
	head, err := repo.Head()
	if err != nil {
		t.Fatal(err)
	}
	remoteRef := plumbing.NewHashReference(plumbing.ReferenceName("refs/remotes/origin/master"), head.Hash())
	if err := repo.Storer.SetReference(remoteRef); err != nil {
		t.Fatal(err)
	}

	pointers, err := lfsPointersForFetch(repo, "origin")
	if err != nil {
		t.Fatal(err)
	}
	// Should find the pointer both from HEAD and from the remote ref
	if len(pointers) < 1 {
		t.Fatalf("expected at least 1 pointer for fetch, got %d", len(pointers))
	}
	if _, ok := pointers[pointer.OID]; !ok {
		t.Fatalf("expected pointer OID %s in fetch pointers", pointer.OID)
	}
}

// --------------------------------------------------------------------------
// Integration test: full LFS workflow with real git operations
// --------------------------------------------------------------------------

func TestRealRepo_FullLFSWorkflow(t *testing.T) {
	root := t.TempDir()
	remotePath := filepath.Join(root, "remote.git")
	repoPath := filepath.Join(root, "repo")
	clonePath := filepath.Join(root, "clone")

	// Set up bare remote
	_, err := git.PlainInit(remotePath, true)
	if err != nil {
		t.Fatal(err)
	}

	// Set up source repo with LFS
	repo, err := git.PlainInit(repoPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{remotePath}}); err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(repoPath, ".gitattributes"), "*.bin filter=lfs diff=lfs merge=lfs -text\n")
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	sig := &object.Signature{Name: "Test", Email: "test@example.com", When: time.Now()}

	if _, err := worktree.Add(".gitattributes"); err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Commit("add gitattributes", &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatal(err)
	}

	// Add a LFS file
	content1 := strings.Repeat("LFS workflow test data\n", 2048)
	writeFile(t, filepath.Join(repoPath, "data.bin"), content1)
	if _, err := worktree.Add("data.bin"); err != nil {
		t.Fatal(err)
	}
	cleaned, err := cleanLFSIndex(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned) != 1 || cleaned[0] != "data.bin" {
		t.Fatalf("expected data.bin to be LFS-cleaned, got %v", cleaned)
	}
	if _, err := worktree.Commit("add LFS data", &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatal(err)
	}

	// Verify LFS object is stored
	pointer1, err := cacheLFSFile(repoPath, "data.bin")
	if err != nil {
		t.Fatal(err)
	}
	if !validLFSObject(repoPath, pointer1) {
		t.Fatal("LFS object should be valid after caching")
	}

	// Verify index has pointer, worktree has real content
	indexData, err := readIndexFile(repo, "data.bin")
	if err != nil {
		t.Fatal(err)
	}
	idxPointer, ok := decodeLFSPointer(indexData)
	if !ok {
		t.Fatalf("expected index to contain LFS pointer")
	}
	if idxPointer.OID != pointer1.OID {
		t.Fatalf("index pointer OID mismatch")
	}
	worktreeData, err := os.ReadFile(filepath.Join(repoPath, "data.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(worktreeData) != content1 {
		t.Fatal("worktree should still have real content")
	}

	// Push to remote
	if err := repo.Push(&git.PushOptions{RemoteName: "origin"}); err != nil {
		t.Fatal(err)
	}

	// Clone the remote repo
	cloneRepo, err := git.PlainClone(clonePath, false, &git.CloneOptions{
		URL: remotePath,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Verify the cloned repo has LFS pointer in the file
	clonedData, err := os.ReadFile(filepath.Join(clonePath, "data.bin"))
	if err != nil {
		t.Fatal(err)
	}
	clonedPointer, ok := decodeLFSPointer(clonedData)
	if !ok {
		t.Fatalf("expected cloned file to be LFS pointer (not hydrated without LFS server), got %q", string(clonedData))
	}
	if clonedPointer.OID != pointer1.OID {
		t.Fatalf("cloned pointer OID = %q, want %q", clonedPointer.OID, pointer1.OID)
	}

	// Manually copy LFS objects to clone and hydrate
	writeFile(t, lfsObjectPath(clonePath, pointer1.OID), content1)
	if err := hydrateLFSFile(clonePath, "data.bin", pointer1); err != nil {
		t.Fatal(err)
	}
	hydrated, err := os.ReadFile(filepath.Join(clonePath, "data.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(hydrated) != content1 {
		t.Fatalf("hydrated content mismatch")
	}

	// Dehydrate and verify pointer is written back
	dehydrated, err := dehydrateCleanLFSFiles(clonePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(dehydrated) != 1 {
		t.Fatalf("expected 1 dehydrated file, got %d", len(dehydrated))
	}
	dehydratedData, err := os.ReadFile(filepath.Join(clonePath, "data.bin"))
	if err != nil {
		t.Fatal(err)
	}
	dehydratedPointer, ok := decodeLFSPointer(dehydratedData)
	if !ok {
		t.Fatal("expected dehydrated file to be LFS pointer")
	}
	if dehydratedPointer.OID != pointer1.OID {
		t.Fatalf("dehydrated pointer OID mismatch")
	}

	// Rehydrate
	if err := rehydrateLFSFiles(clonePath, dehydrated); err != nil {
		t.Fatal(err)
	}
	rehydrated, err := os.ReadFile(filepath.Join(clonePath, "data.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(rehydrated) != content1 {
		t.Fatalf("rehydrated content mismatch")
	}

	// Test normalizeLFSStatus
	cloneWorktree, err := cloneRepo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	status, err := cloneWorktree.Status()
	if err != nil {
		t.Fatal(err)
	}
	if err := normalizeLFSStatus(clonePath, status); err != nil {
		t.Fatal(err)
	}
	if fileStatus, ok := status["data.bin"]; ok {
		if fileStatus.Worktree != git.Unmodified {
			t.Fatalf("expected hydrated file to be unmodified after normalize, got %q", fileStatus.Worktree)
		}
	}

	// Add a second LFS file and verify pointers at HEAD
	content2 := strings.Repeat("Second LFS file\n", 1024)
	writeFile(t, filepath.Join(clonePath, "extra.bin"), content2)
	cloneWorktree2, err := cloneRepo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cloneWorktree2.Add("extra.bin"); err != nil {
		t.Fatal(err)
	}
	cleaned2, err := cleanLFSIndex(clonePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(cleaned2) != 1 || cleaned2[0] != "extra.bin" {
		t.Fatalf("expected extra.bin to be LFS-cleaned, got %v", cleaned2)
	}
	if _, err := cloneWorktree2.Commit("add extra LFS file", &git.CommitOptions{Author: sig, Committer: sig}); err != nil {
		t.Fatal(err)
	}

	pointers, err := lfsPointersAtHead(cloneRepo)
	if err != nil {
		t.Fatal(err)
	}
	if len(pointers) != 2 {
		t.Fatalf("expected 2 LFS pointers at HEAD, got %d: %+v", len(pointers), pointers)
	}
	if _, ok := pointers["data.bin"]; !ok {
		t.Fatal("expected data.bin in HEAD pointers")
	}
	if _, ok := pointers["extra.bin"]; !ok {
		t.Fatal("expected extra.bin in HEAD pointers")
	}
}

// --------------------------------------------------------------------------
// Integration test: clone a real remote LFS-enabled repo
//
// Run with:
//   TEST_LFS_REMOTE_REPO=https://github.com/some/lfs-repo go test -run TestRealRemote_LFSClone
//
// With authentication:
//   TEST_LFS_REMOTE_REPO=https://github.com/some/lfs-repo \
//   TEST_LFS_AUTH_TYPE=token \
//   TEST_LFS_TOKEN=ghp_xxx \
//   go test -run TestRealRemote_LFSClone
// --------------------------------------------------------------------------

func remoteAuthOptions(t *testing.T) string {
	t.Helper()
	authType := os.Getenv("TEST_LFS_AUTH_TYPE")
	if authType == "" {
		return ""
	}
	username := os.Getenv("TEST_LFS_AUTH_USERNAME")
	password := os.Getenv("TEST_LFS_AUTH_PASSWORD")
	token := os.Getenv("TEST_LFS_AUTH_TOKEN")
	options := runOptions{Auth: gitAuth{Type: authType, Username: username, Password: password, Token: token}}
	data, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestRealRemote_LFSClone(t *testing.T) {
	repoURL := os.Getenv("TEST_LFS_REMOTE_REPO")
	if repoURL == "" {
		t.Skip("TEST_LFS_REMOTE_REPO not set; skipping real remote LFS integration test")
	}
	if !strings.HasPrefix(repoURL, "https://") {
		t.Skip("TEST_LFS_REMOTE_REPO must be an HTTPS URL")
	}

	root := t.TempDir()
	cloneDir := filepath.Join(root, "lfs-clone")
	options := remoteAuthOptions(t)

	cloneResult := waitRun(t, root, "git clone "+quoteArg(repoURL)+" lfs-clone", options)
	cloneData := resultData(t, cloneResult)

	if cloneData["path"] != cloneDir {
		t.Fatalf("clone path = %q, want %q", cloneData["path"], cloneDir)
	}

	lfsRaw := cloneData["lfs"]
	if lfsRaw == nil {
		t.Log("No LFS data in clone result (repo may not have LFS objects, or objects already present)")
		return
	}
	lfsData, ok := lfsRaw.(map[string]any)
	if !ok {
		t.Fatalf("LFS data has unexpected type: %T", lfsRaw)
	}
	t.Logf("LFS clone result: objects=%v, downloaded=%v, hydrated=%v",
		lfsData["objects"], lfsData["downloaded"], lfsData["hydrated"])

	_, err := git.PlainOpen(cloneDir)
	if err != nil {
		t.Fatalf("cloned repo is not a valid git repo: %v", err)
	}

	statusResult := waitRun(t, cloneDir, "git status", "")
	statusData := resultData(t, statusResult)
	if clean, _ := statusData["clean"].(bool); !clean {
		t.Fatalf("expected clean worktree after clone, got %#v", statusData)
	}
}

func TestRealRemote_LFSPull(t *testing.T) {
	repoURL := os.Getenv("TEST_LFS_REMOTE_REPO")
	if repoURL == "" {
		t.Skip("TEST_LFS_REMOTE_REPO not set; skipping real remote LFS pull test")
	}
	if !strings.HasPrefix(repoURL, "https://") {
		t.Skip("TEST_LFS_REMOTE_REPO must be an HTTPS URL")
	}

	root := t.TempDir()
	cloneDir := filepath.Join(root, "lfs-clone")
	options := remoteAuthOptions(t)

	waitRun(t, root, "git clone "+quoteArg(repoURL)+" lfs-clone", options)

	fetchResult := waitRun(t, cloneDir, "git fetch origin", options)
	fetchData := resultData(t, fetchResult)
	t.Logf("Fetch result: upToDate=%v, lfs=%v", fetchData["upToDate"], fetchData["lfs"])

	pullResult := waitRun(t, cloneDir, "git pull origin master", options)
	pullData := resultData(t, pullResult)
	t.Logf("Pull result: lfs=%v", pullData["lfs"])

	statusResult := waitRun(t, cloneDir, "git status", "")
	statusData := resultData(t, statusResult)
	if clean, _ := statusData["clean"].(bool); !clean {
		t.Fatalf("expected clean worktree after pull, got %#v", statusData)
	}
}

func TestRealRemote_LFSLog(t *testing.T) {
	repoURL := os.Getenv("TEST_LFS_REMOTE_REPO")
	if repoURL == "" {
		t.Skip("TEST_LFS_REMOTE_REPO not set; skipping real remote LFS log test")
	}
	if !strings.HasPrefix(repoURL, "https://") {
		t.Skip("TEST_LFS_REMOTE_REPO must be an HTTPS URL")
	}

	root := t.TempDir()
	cloneDir := filepath.Join(root, "lfs-clone")
	options := remoteAuthOptions(t)

	waitRun(t, root, "git clone "+quoteArg(repoURL)+" lfs-clone", options)

	logResult := waitRun(t, cloneDir, "git log --limit 5", "")
	logData := resultData(t, logResult)
	commits, ok := logData["commits"].([]any)
	if !ok || len(commits) == 0 {
		t.Fatalf("expected commits in log, got %#v", logData)
	}
	t.Logf("Log returned %d commits", len(commits))

	repo, err := git.PlainOpen(cloneDir)
	if err != nil {
		t.Fatal(err)
	}
	pointers, err := lfsPointersAtHead(repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Found %d LFS pointer(s) at HEAD", len(pointers))
	for name, ptr := range pointers {
		t.Logf("  %s: oid=%s size=%d", name, ptr.OID[:12]+"...", ptr.Size)
	}
}

// --------------------------------------------------------------------------
// Test LFS auth
// --------------------------------------------------------------------------

func newHTTPRequest(t *testing.T, method, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestRealRepo_ApplyLFSAuth_Basic(t *testing.T) {
	root := t.TempDir()
	_, _ = git.PlainInit(root, false)

	req := newHTTPRequest(t, "GET", "http://localhost/test")
	options := runOptions{Auth: gitAuth{Type: "basic", Username: "user", Password: "pass"}}
	applyLFSAuth(req, options)

	user, pass, ok := req.BasicAuth()
	if !ok || user != "user" || pass != "pass" {
		t.Fatalf("expected basic auth user/pass, got %q/%q", user, pass)
	}
}

func TestRealRepo_ApplyLFSAuth_Token(t *testing.T) {
	req := newHTTPRequest(t, "GET", "http://localhost/test")
	options := runOptions{Auth: gitAuth{Type: "token", Token: "mytoken"}}
	applyLFSAuth(req, options)

	user, pass, ok := req.BasicAuth()
	if !ok || user != "token" || pass != "mytoken" {
		t.Fatalf("expected token auth, got user=%q pass=%q", user, pass)
	}
}

func TestRealRepo_ApplyLFSAuth_TokenWithUsername(t *testing.T) {
	req := newHTTPRequest(t, "GET", "http://localhost/test")
	options := runOptions{Auth: gitAuth{Type: "token", Username: "custom", Token: "mytoken"}}
	applyLFSAuth(req, options)

	user, pass, ok := req.BasicAuth()
	if !ok || user != "custom" || pass != "mytoken" {
		t.Fatalf("expected custom username with token, got user=%q pass=%q", user, pass)
	}
}

func TestRealRepo_ApplyLFSAuth_Empty(t *testing.T) {
	req := newHTTPRequest(t, "GET", "http://localhost/test")
	options := runOptions{Auth: gitAuth{Type: "none"}}
	applyLFSAuth(req, options)

	user, pass, ok := req.BasicAuth()
	if ok {
		t.Fatalf("expected no auth, got user=%q pass=%q", user, pass)
	}
}

func TestRealRepo_SafeLFSOptions_SameHost(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{"https://github.com/user/repo.git"},
	})
	if err != nil {
		t.Fatal(err)
	}

	options := runOptions{Auth: gitAuth{Type: "basic", Username: "u", Password: "p"}}
	result := safeLFSOptions(repo, "origin", "https://github.com/user/repo.git/info/lfs/objects/batch", options)
	if result.Auth.Username != "u" {
		t.Fatal("expected auth to be preserved for same-host target")
	}
}

func TestRealRepo_SafeLFSOptions_DifferentHost(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{"https://github.com/user/repo.git"},
	})
	if err != nil {
		t.Fatal(err)
	}

	options := runOptions{Auth: gitAuth{Type: "basic", Username: "u", Password: "p"}}
	result := safeLFSOptions(repo, "origin", "https://evil.com/steal-creds", options)
	if result.Auth.Username != "" {
		t.Fatal("expected auth to be stripped for different-host target")
	}
}

func TestRealRepo_FirstRemoteName(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.CreateRemote(&config.RemoteConfig{
		Name: "upstream",
		URLs: []string{"https://example.com/repo.git"},
	})
	if err != nil {
		t.Fatal(err)
	}

	name := firstRemoteName(repo)
	if name != "upstream" {
		t.Fatalf("firstRemoteName = %q, want %q", name, "upstream")
	}
}

func TestRealRepo_FirstRemoteName_OriginPreferred(t *testing.T) {
	root := t.TempDir()
	repo, err := git.PlainInit(root, false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.CreateRemote(&config.RemoteConfig{
		Name: "upstream",
		URLs: []string{"https://example.com/repo.git"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{"https://github.com/user/repo.git"},
	})
	if err != nil {
		t.Fatal(err)
	}

	name := firstRemoteName(repo)
	if name != "origin" {
		t.Fatalf("firstRemoteName = %q, want %q (origin preferred)", name, "origin")
	}
}
