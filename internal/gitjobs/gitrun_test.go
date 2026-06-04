package gitjobs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestGitRunLocalWorkflow(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source repo")
	sourceHead := initRepository(t, source, "README.md", "hello\n", "initial")

	parent := t.TempDir()
	dest := filepath.Join(parent, "source repo")
	clone := waitRun(t, parent, "git clone "+quoteArg(source), "")
	if path := resultData(t, clone)["path"].(string); path != dest {
		t.Fatalf("expected inferred clone path %q, got %q", dest, path)
	}

	status := waitRun(t, dest, "git status", "")
	data := resultData(t, status)
	if clean, _ := data["clean"].(bool); !clean {
		t.Fatalf("expected clean clone, got %#v", data)
	}

	writeFile(t, filepath.Join(dest, "Assets", "Main.wa"), "func main {}\n")
	waitRun(t, dest, "git add .", "")
	commit := waitRun(t, dest, `git commit -m "Add Wa entry" --author-name Dora --author-email dora@example.com`, "")
	commitHash := resultData(t, commit)["commit"].(string)
	if commitHash == "" || commitHash == sourceHead {
		t.Fatalf("unexpected commit hash %q", commitHash)
	}

	logResult := waitRun(t, dest, "git log --limit 2", "")
	commits := resultData(t, logResult)["commits"].([]any)
	if len(commits) != 2 {
		t.Fatalf("expected 2 commits, got %#v", commits)
	}

	waitRun(t, dest, "git reset --hard "+sourceHead+" --confirm", "")
	if _, err := os.Stat(filepath.Join(dest, "Assets", "Main.wa")); !os.IsNotExist(err) {
		t.Fatalf("expected reset to remove committed file, stat err=%v", err)
	}

	waitRun(t, dest, "git branch docs", "")
	branchResult := waitRun(t, dest, "git branch", "")
	if !containsNamedItem(resultData(t, branchResult)["branches"], "docs") {
		t.Fatalf("expected branch list to include docs, got %#v", resultData(t, branchResult))
	}
	waitRun(t, dest, "git branch -d docs", "")
	branchResult = waitRun(t, dest, "git branch", "")
	if containsNamedItem(resultData(t, branchResult)["branches"], "docs") {
		t.Fatalf("expected branch list to exclude deleted docs, got %#v", resultData(t, branchResult))
	}

	waitRun(t, dest, "git tag v1", "")
	tagResult := waitRun(t, dest, "git tag", "")
	if !containsNamedItem(resultData(t, tagResult)["tags"], "v1") {
		t.Fatalf("expected tag list to include v1, got %#v", resultData(t, tagResult))
	}
	waitRun(t, dest, `git tag -a v2 -m "Version 2"`, "")
	tagResult = waitRun(t, dest, "git tag", "")
	if !containsNamedItem(resultData(t, tagResult)["tags"], "v2") {
		t.Fatalf("expected tag list to include annotated v2, got %#v", resultData(t, tagResult))
	}
	waitRun(t, dest, "git tag -d v1", "")
	tagResult = waitRun(t, dest, "git tag", "")
	if containsNamedItem(resultData(t, tagResult)["tags"], "v1") {
		t.Fatalf("expected tag list to exclude deleted v1, got %#v", resultData(t, tagResult))
	}

	remoteURL := filepath.Join(t.TempDir(), "secondary.git")
	waitRun(t, dest, "git remote add backup "+quoteArg(remoteURL), "")
	remoteResult := waitRun(t, dest, "git remote -v", "")
	if !containsNamedItem(resultData(t, remoteResult)["remotes"], "backup") {
		t.Fatalf("expected remote list to include backup, got %#v", resultData(t, remoteResult))
	}
	updatedRemoteURL := filepath.Join(t.TempDir(), "secondary-updated.git")
	waitRun(t, dest, "git remote set-url backup "+quoteArg(updatedRemoteURL), "")
	remoteResult = waitRun(t, dest, "git remote -v", "")
	if !containsRemoteURL(resultData(t, remoteResult)["remotes"], "backup", updatedRemoteURL) {
		t.Fatalf("expected backup remote URL to be updated, got %#v", resultData(t, remoteResult))
	}
	waitRun(t, dest, "git remote remove backup", "")
	remoteResult = waitRun(t, dest, "git remote -v", "")
	if containsNamedItem(resultData(t, remoteResult)["remotes"], "backup") {
		t.Fatalf("expected remote list to exclude removed backup, got %#v", resultData(t, remoteResult))
	}

	spacedPath := filepath.Join(dest, "Assets", "My File.wa")
	writeFile(t, spacedPath, "func spaced {}\n")
	waitRun(t, dest, `git add "Assets/My File.wa"`, "")
	waitRun(t, dest, `git commit -m "Add spaced path"`, "")
	waitRun(t, dest, `git rm "Assets/My File.wa"`, "")
	waitRun(t, dest, `git commit -m "Remove spaced path"`, "")
	if _, err := os.Stat(spacedPath); !os.IsNotExist(err) {
		t.Fatalf("expected quoted git rm to remove spaced path, stat err=%v", err)
	}

	moveFrom := filepath.Join(dest, "move from.txt")
	moveTo := filepath.Join(dest, "move to.txt")
	writeFile(t, moveFrom, "move me\n")
	waitRun(t, dest, `git add "move from.txt"`, "")
	waitRun(t, dest, `git commit -m "Add move source"`, "")
	waitRun(t, dest, `git mv "move from.txt" "move to.txt"`, "")
	waitRun(t, dest, `git commit -m "Move file"`, "")
	if _, err := os.Stat(moveFrom); !os.IsNotExist(err) {
		t.Fatalf("expected git mv to remove source path, stat err=%v", err)
	}
	if _, err := os.Stat(moveTo); err != nil {
		t.Fatalf("expected git mv to create destination path: %v", err)
	}

	writeFile(t, moveTo, "changed\n")
	waitRun(t, dest, `git restore "move to.txt"`, "")
	content, err := os.ReadFile(moveTo)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "move me\n" {
		t.Fatalf("expected restore to reset worktree file, got %q", string(content))
	}
	writeFile(t, moveTo, "staged change\n")
	waitRun(t, dest, `git add "move to.txt"`, "")
	waitRun(t, dest, `git restore --staged "move to.txt"`, "")
	waitRun(t, dest, `git restore "move to.txt"`, "")

	waitRun(t, dest, "git checkout -b feature", "")
	writeFile(t, filepath.Join(dest, "feature.txt"), "feature\n")
	waitRun(t, dest, "git add feature.txt", "")
	waitRun(t, dest, `git commit -m "Add feature file"`, "")
	waitRun(t, dest, "git checkout master --force", "")
	if _, err := os.Stat(filepath.Join(dest, "feature.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected checkout master to remove feature file, stat err=%v", err)
	}
	waitRun(t, dest, "git checkout feature --force", "")
	if _, err := os.Stat(filepath.Join(dest, "feature.txt")); err != nil {
		t.Fatalf("expected checkout feature to restore feature file: %v", err)
	}
	waitRun(t, dest, "git rm feature.txt", "")
	waitRun(t, dest, `git commit -m "Remove feature file"`, "")
	if _, err := os.Stat(filepath.Join(dest, "feature.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected git rm to remove feature file, stat err=%v", err)
	}

	writeFile(t, filepath.Join(dest, "scratch.tmp"), "temporary\n")
	waitRun(t, dest, "git clean -f", "")
	if _, err := os.Stat(filepath.Join(dest, "scratch.tmp")); !os.IsNotExist(err) {
		t.Fatalf("expected clean to remove untracked file, stat err=%v", err)
	}
}

func TestGitRunPushAndPull(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	parentA := filepath.Join(root, "parent-a")
	parentB := filepath.Join(root, "parent-b")
	cloneA := filepath.Join(parentA, "clone-a")
	cloneB := filepath.Join(parentB, "clone-b")

	_, err := git.PlainInit(remote, true)
	if err != nil {
		t.Fatal(err)
	}
	seedRepo, err := git.PlainInit(seed, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedRepo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{remote}}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(seed, "README.md"), "base\n")
	seedWorktree, err := seedRepo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedWorktree.Add("README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := seedWorktree.Commit("initial", commitOptions()); err != nil {
		t.Fatal(err)
	}
	if err := seedRepo.Push(&git.PushOptions{RemoteName: "origin"}); err != nil {
		t.Fatal(err)
	}

	waitRun(t, parentA, "git clone "+quoteArg(remote)+" clone-a", "")
	waitRun(t, parentB, "git clone "+quoteArg(remote)+" clone-b", "")

	writeFile(t, filepath.Join(cloneA, "README.md"), "base\nfrom clone A\n")
	waitRun(t, cloneA, "git add README.md", "")
	waitRun(t, cloneA, `git commit -m "Update from clone A"`, "")
	waitRun(t, cloneA, "git push origin master", "")

	waitRun(t, cloneB, "git fetch origin", "")
	waitRun(t, cloneB, "git pull origin master", "")
	content, err := os.ReadFile(filepath.Join(cloneB, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "from clone A") {
		t.Fatalf("pull did not update clone-b README: %q", string(content))
	}

	lsRemote := waitRun(t, root, "git ls-remote "+quoteArg(remote), "")
	if !containsRefName(resultData(t, lsRemote)["refs"], "refs/heads/master") {
		t.Fatalf("expected ls-remote refs to include master, got %#v", resultData(t, lsRemote))
	}
}

func TestGitRunInit(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "non-empty")
	writeFile(t, filepath.Join(repo, "README.md"), "hello\n")
	result := waitRun(t, repo, "git init", "")
	data := resultData(t, result)
	if bare, _ := data["bare"].(bool); bare {
		t.Fatalf("expected non-bare init, got %#v", data)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git")); err != nil {
		t.Fatalf("expected git init to create .git in non-empty directory: %v", err)
	}

	bareRepo := filepath.Join(t.TempDir(), "bare.git")
	result = waitRun(t, bareRepo, "git init --bare", "")
	data = resultData(t, result)
	if bare, _ := data["bare"].(bool); !bare {
		t.Fatalf("expected bare init, got %#v", data)
	}
	if _, err := os.Stat(filepath.Join(bareRepo, "HEAD")); err != nil {
		t.Fatalf("expected bare init to create HEAD: %v", err)
	}
}

func TestGitRunRejectsShellSyntax(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initRepository(t, repo, "README.md", "hello\n", "initial")
	result := waitRun(t, repo, "git status && git push", "")
	if result.State != StateError {
		t.Fatalf("expected shell syntax rejection, got %#v", result)
	}
}

func waitRun(t *testing.T, repoPath, command, options string) pollResult {
	t.Helper()
	id := StartRun(repoPath, command, options)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var result pollResult
		if err := json.Unmarshal([]byte(Poll(id)), &result); err != nil {
			t.Fatal(err)
		}
		switch result.State {
		case StateDone:
			Dispose(id)
			return result
		case StateError, StateCanceled:
			Dispose(id)
			if command == "git status && git push" {
				return result
			}
			t.Fatalf("%s failed: %s", command, result.Error)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s timed out with state %s", command, result.State)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func resultData(t *testing.T, result pollResult) map[string]any {
	t.Helper()
	data, ok := result.Data.(map[string]any)
	if !ok {
		t.Fatalf("missing result data in %#v", result)
	}
	return data
}

func containsNamedItem(value any, name string) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		data, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if data["name"] == name {
			return true
		}
	}
	return false
}

func containsRemoteURL(value any, name, url string) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		data, ok := item.(map[string]any)
		if !ok || data["name"] != name {
			continue
		}
		urls, ok := data["urls"].([]any)
		if !ok {
			return false
		}
		for _, item := range urls {
			if item == url {
				return true
			}
		}
	}
	return false
}

func containsRefName(value any, name string) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		data, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if data["name"] == name {
			return true
		}
	}
	return false
}

func initRepository(t *testing.T, path, name, content, message string) string {
	t.Helper()
	repo, err := git.PlainInit(path, false)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(path, name), content)
	worktree, err := repo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := worktree.Add(name); err != nil {
		t.Fatal(err)
	}
	hash, err := worktree.Commit(message, commitOptions())
	if err != nil {
		t.Fatal(err)
	}
	return hash.String()
}

func commitOptions() *git.CommitOptions {
	signature := &object.Signature{
		Name:  "Dora",
		Email: "dora@example.com",
		When:  time.Now(),
	}
	return &git.CommitOptions{Author: signature, Committer: signature}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0666); err != nil {
		t.Fatal(err)
	}
}

func quoteArg(value string) string {
	return fmt.Sprintf("%q", value)
}
