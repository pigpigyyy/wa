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
	"github.com/go-git/go-git/v5/plumbing"
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

	writeFile(t, filepath.Join(dest, "README.md"), "hello\nworking\n")
	diff := resultData(t, waitRun(t, dest, "git diff -- README.md", ""))
	if diff["oldText"] != "hello\n" || diff["newText"] != "hello\nworking\n" {
		t.Fatalf("unexpected unstaged diff content: %#v", diff)
	}
	waitRun(t, dest, "git add README.md", "")
	stagedDiff := resultData(t, waitRun(t, dest, "git diff --staged -- README.md", ""))
	if stagedDiff["oldText"] != "hello\n" || stagedDiff["newText"] != "hello\nworking\n" {
		t.Fatalf("unexpected staged diff content: %#v", stagedDiff)
	}
	waitRun(t, dest, `git commit -m "Update readme"`, "")

	writeFile(t, filepath.Join(dest, "new.txt"), "new file\n")
	untrackedDiff := resultData(t, waitRun(t, dest, "git diff -- new.txt", ""))
	if untrackedDiff["oldText"] != "" || untrackedDiff["newText"] != "new file\n" {
		t.Fatalf("unexpected untracked diff content: %#v", untrackedDiff)
	}
	writeFile(t, filepath.Join(dest, "large.txt"), strings.Repeat("x", maxPreviewDiffBytes+1))
	largeDiff := resultData(t, waitRun(t, dest, "git diff -- large.txt", ""))
	if largeDiff["mode"] != "large" || largeDiff["newText"] != nil {
		t.Fatalf("expected large diff to skip text preview, got %#v", largeDiff)
	}
	if err := os.Remove(filepath.Join(dest, "large.txt")); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(dest, "binary.bin")
	if err := os.WriteFile(binaryPath, []byte{0, 1, 2, 3}, 0666); err != nil {
		t.Fatal(err)
	}
	binaryDiff := resultData(t, waitRun(t, dest, "git diff -- binary.bin", ""))
	if binaryDiff["mode"] != "binary" || binaryDiff["oldSize"] != float64(0) || binaryDiff["newSize"] != float64(4) {
		t.Fatalf("expected binary diff size metadata, got %#v", binaryDiff)
	}
	if err := os.Remove(binaryPath); err != nil {
		t.Fatal(err)
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
	latestCommit := commits[0].(map[string]any)
	if !containsCommitFile(latestCommit["files"], "Assets/Main.wa", "A") {
		t.Fatalf("expected latest commit files to include Assets/Main.wa add, got %#v", latestCommit["files"])
	}
	commitDiff := resultData(t, waitRun(t, dest, "git diff "+commitHash+" -- Assets/Main.wa", ""))
	if commitDiff["oldText"] != "" || commitDiff["newText"] != "func main {}\n" {
		t.Fatalf("unexpected commit diff content: %#v", commitDiff)
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
	if !containsRemoteBranch(resultData(t, branchResult)["branches"], "origin", "master") {
		t.Fatalf("expected branch list to include origin/master, got %#v", resultData(t, branchResult))
	}
	waitRun(t, dest, "git checkout origin/master", "")
	waitRun(t, dest, "git checkout master", "")
	waitRun(t, dest, "git checkout -b remote-main origin/master", "")
	assertBranchUpstream(t, dest, "remote-main", "origin", "refs/heads/master")
	waitRun(t, dest, "git checkout master", "")
	waitRun(t, dest, "git branch -d remote-main", "")
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
	statusResult := waitRun(t, dest, "git status", "")
	statusFiles := resultData(t, statusResult)["files"].([]any)
	if !containsStatus(statusFiles, "move to.txt", "M", " ") {
		t.Fatalf("expected modified file to be staged, got %#v", statusFiles)
	}
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

func TestGitRunShallowCloneStatus(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	parent := filepath.Join(root, "parent")
	clonePath := filepath.Join(parent, "shallow")

	sourceHead := initRepository(t, source, "README.md", "initial\n", "initial")
	repo, worktree, err := openWorktree(source)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(source, "README.md"), "initial\nsecond\n")
	if _, err := worktree.Add("README.md"); err != nil {
		t.Fatal(err)
	}
	secondHash, err := worktree.Commit("second", commitOptions())
	if err != nil {
		t.Fatal(err)
	}
	if secondHash.String() == sourceHead {
		t.Fatal("expected second commit")
	}
	if _, err := repo.Head(); err != nil {
		t.Fatal(err)
	}

	clone := waitRun(t, parent, "git clone --depth 1 "+quoteArg(source)+" shallow", "")
	if path := resultData(t, clone)["path"].(string); path != clonePath {
		t.Fatalf("expected shallow clone path %q, got %q", clonePath, path)
	}

	status := resultData(t, waitRun(t, clonePath, "git status", ""))
	if clean, _ := status["clean"].(bool); !clean {
		t.Fatalf("expected clean shallow clone, got %#v", status)
	}
	branch := resultData(t, waitRun(t, clonePath, "git branch", ""))
	if !containsNamedItem(branch["branches"], "master") {
		t.Fatalf("expected shallow clone branch list to include master, got %#v", branch)
	}
	log := resultData(t, waitRun(t, clonePath, "git log -n 100", ""))
	commits := log["commits"].([]any)
	if len(commits) != 1 {
		t.Fatalf("expected shallow clone log to include only latest commit, got %#v", log)
	}
	shallowTip := commits[0].(map[string]any)
	if shallowTip["hash"] != secondHash.String() {
		t.Fatalf("expected shallow tip to be second commit, got %#v", shallowTip)
	}
	if !containsCommitFile(shallowTip["files"], "README.md", "A") {
		t.Fatalf("expected shallow boundary commit to list README.md as added, got %#v", shallowTip["files"])
	}
	pathLog := resultData(t, waitRun(t, clonePath, "git log -n 100 -- README.md", ""))
	if pathCommits := pathLog["commits"].([]any); len(pathCommits) != 1 || pathCommits[0].(map[string]any)["hash"] != secondHash.String() {
		t.Fatalf("expected shallow path log to include boundary commit, got %#v", pathLog)
	}
	boundaryDiff := resultData(t, waitRun(t, clonePath, "git diff "+secondHash.String()+" -- README.md", ""))
	if boundaryDiff["oldText"] != "" || boundaryDiff["newText"] != "initial\nsecond\n" {
		t.Fatalf("unexpected shallow boundary diff content: %#v", boundaryDiff)
	}

	writeFile(t, filepath.Join(source, "README.md"), "initial\nsecond\nthird\n")
	if _, err := worktree.Add("README.md"); err != nil {
		t.Fatal(err)
	}
	thirdHash, err := worktree.Commit("third", commitOptions())
	if err != nil {
		t.Fatal(err)
	}
	pull := resultData(t, waitRun(t, clonePath, "git pull origin master", ""))
	if pull["lfs"] == nil {
		t.Fatalf("expected shallow pull to return LFS metadata, got %#v", pull)
	}
	content, err := os.ReadFile(filepath.Join(clonePath, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "initial\nsecond\nthird\n" {
		t.Fatalf("expected shallow pull to update README.md, got %q", string(content))
	}

	writeFile(t, filepath.Join(clonePath, "local.txt"), "one\n")
	waitRun(t, clonePath, "git add local.txt", "")
	localOne := resultData(t, waitRun(t, clonePath, `git commit -m "local one"`, ""))["commit"].(string)
	writeFile(t, filepath.Join(clonePath, "local.txt"), "one\ntwo\n")
	waitRun(t, clonePath, "git add local.txt", "")
	localTwo := resultData(t, waitRun(t, clonePath, `git commit -m "local two"`, ""))["commit"].(string)
	log = resultData(t, waitRun(t, clonePath, "git log -n 100", ""))
	commits = log["commits"].([]any)
	if len(commits) != 3 {
		t.Fatalf("expected shallow clone log to include local commits and shallow tip, got %#v", log)
	}
	if commits[0].(map[string]any)["hash"] != localTwo || commits[1].(map[string]any)["hash"] != localOne {
		t.Fatalf("expected local commits before shallow tip, got %#v", commits)
	}
	fetch := resultData(t, waitRun(t, clonePath, "git fetch origin", ""))
	if fetch["lfs"] == nil {
		t.Fatalf("expected shallow fetch to return LFS metadata, got %#v", fetch)
	}
	writeFile(t, filepath.Join(source, "README.md"), "initial\nsecond\nthird\nfourth\n")
	if _, err := worktree.Add("README.md"); err != nil {
		t.Fatal(err)
	}
	fourthHash, err := worktree.Commit("fourth", commitOptions())
	if err != nil {
		t.Fatal(err)
	}
	fetch = resultData(t, waitRun(t, clonePath, "git fetch origin", ""))
	if fetch["lfs"] == nil {
		t.Fatalf("expected updated shallow fetch to return LFS metadata, got %#v", fetch)
	}
	branch = resultData(t, waitRun(t, clonePath, "git branch", ""))
	if !containsBranchHash(branch["branches"], "origin", "master", fourthHash.String()) {
		t.Fatalf("expected origin/master to move to fourth commit, got %#v", branch)
	}
	if thirdHash.String() == fourthHash.String() {
		t.Fatal("expected source commits to differ")
	}
}

func TestGitRunShallowClonePush(t *testing.T) {
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	seed := filepath.Join(root, "seed")
	parent := filepath.Join(root, "parent")
	clonePath := filepath.Join(parent, "shallow")
	verifyParent := filepath.Join(root, "verify-parent")
	verifyPath := filepath.Join(verifyParent, "verify")

	if _, err := git.PlainInit(remote, true); err != nil {
		t.Fatal(err)
	}
	seedRepo, err := git.PlainInit(seed, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := seedRepo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{remote}}); err != nil {
		t.Fatal(err)
	}
	seedWorktree, err := seedRepo.Worktree()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(seed, "README.md"), "initial\n")
	if _, err := seedWorktree.Add("README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := seedWorktree.Commit("initial", commitOptions()); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(seed, "README.md"), "initial\nsecond\n")
	if _, err := seedWorktree.Add("README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := seedWorktree.Commit("second", commitOptions()); err != nil {
		t.Fatal(err)
	}
	if err := seedRepo.Push(&git.PushOptions{RemoteName: "origin"}); err != nil {
		t.Fatal(err)
	}

	waitRun(t, parent, "git clone --depth 1 "+quoteArg(remote)+" shallow", "")
	writeFile(t, filepath.Join(clonePath, "local.txt"), "local\n")
	waitRun(t, clonePath, "git add local.txt", "")
	localCommit := resultData(t, waitRun(t, clonePath, `git commit -m "local commit"`, ""))["commit"].(string)
	waitRun(t, clonePath, "git push origin master", "")

	waitRun(t, verifyParent, "git clone "+quoteArg(remote)+" verify", "")
	log := resultData(t, waitRun(t, verifyPath, "git log -n 1", ""))
	commits := log["commits"].([]any)
	if len(commits) != 1 || commits[0].(map[string]any)["hash"] != localCommit {
		t.Fatalf("expected pushed shallow clone commit at remote head, got %#v", log)
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

	waitRun(t, cloneA, "git checkout -b topic", "")
	writeFile(t, filepath.Join(cloneA, "topic.txt"), "topic\n")
	waitRun(t, cloneA, "git add topic.txt", "")
	waitRun(t, cloneA, `git commit -m "Add topic"`, "")
	upstreamResult := waitRun(t, cloneA, "git push -u origin topic", "")
	upstream := resultData(t, upstreamResult)["upstream"].(map[string]any)
	if upstream["branch"] != "topic" || upstream["remote"] != "origin" || upstream["merge"] != "refs/heads/topic" {
		t.Fatalf("expected push -u to report upstream config, got %#v", upstream)
	}
	assertBranchUpstream(t, cloneA, "topic", "origin", "refs/heads/topic")

	waitRun(t, cloneA, "git checkout -b docs", "")
	writeFile(t, filepath.Join(cloneA, "docs.txt"), "docs\n")
	waitRun(t, cloneA, "git add docs.txt", "")
	waitRun(t, cloneA, `git commit -m "Add docs"`, "")
	waitRun(t, cloneA, "git push --set-upstream origin docs", "")
	assertBranchUpstream(t, cloneA, "docs", "origin", "refs/heads/docs")

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

func TestGitRunRestoreStagedOnUnbornBranch(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "unborn")
	waitRun(t, repo, "git init", "")
	writeFile(t, filepath.Join(repo, "hello.txt"), "hello\n")
	waitRun(t, repo, "git add hello.txt", "")

	stagedStatus := resultData(t, waitRun(t, repo, "git status", ""))
	if !containsStatus(stagedStatus["files"], "hello.txt", "A", " ") {
		t.Fatalf("expected hello.txt to be staged before restore, got %#v", stagedStatus)
	}

	waitRun(t, repo, "git restore --staged hello.txt", "")
	unstagedStatus := resultData(t, waitRun(t, repo, "git status", ""))
	if !containsStatus(unstagedStatus["files"], "hello.txt", "?", "?") {
		t.Fatalf("expected hello.txt to be untracked after restore --staged, got %#v", unstagedStatus)
	}
}

func TestGitRunBranchCreateOnUnbornBranch(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "unborn-branch")
	waitRun(t, repo, "git init", "")
	result := waitRun(t, repo, "git branch feature", "")
	data := resultData(t, result)
	if data["branch"] != "feature" || data["unborn"] != true {
		t.Fatalf("expected unborn branch creation result, got %#v", data)
	}

	branchData := resultData(t, waitRun(t, repo, "git branch", ""))
	if branchData["current"] != "feature" {
		t.Fatalf("expected current unborn branch to be feature, got %#v", branchData)
	}

	writeFile(t, filepath.Join(repo, "hello.txt"), "hello\n")
	waitRun(t, repo, "git add hello.txt", "")
	waitRun(t, repo, `git commit -m "initial"`, "")
	branchData = resultData(t, waitRun(t, repo, "git branch", ""))
	if branchData["current"] != "feature" || !containsNamedItem(branchData["branches"], "feature") {
		t.Fatalf("expected first commit to land on feature, got %#v", branchData)
	}
}

func TestGitRunCheckoutCreateOnUnbornBranch(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "unborn-checkout")
	waitRun(t, repo, "git init", "")
	result := waitRun(t, repo, "git checkout -b feature", "")
	data := resultData(t, result)
	if data["branch"] != "feature" || data["unborn"] != true {
		t.Fatalf("expected unborn checkout branch result, got %#v", data)
	}

	branchData := resultData(t, waitRun(t, repo, "git branch", ""))
	if branchData["current"] != "feature" {
		t.Fatalf("expected current unborn branch to be feature, got %#v", branchData)
	}
}

func TestGitRunLogOnUnbornBranchReturnsEmpty(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "unborn-log")
	waitRun(t, repo, "git init", "")
	data := resultData(t, waitRun(t, repo, "git log -n 5", ""))
	commits, ok := data["commits"].([]any)
	if !ok || len(commits) != 0 {
		t.Fatalf("expected empty commit list on unborn branch, got %#v", data)
	}
}

func TestGitRunUnbornHeadClearErrors(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "unborn-errors")
	waitRun(t, repo, "git init", "")

	result := waitRunError(t, repo, "git push origin master", "")
	if !strings.Contains(result.Error, "cannot push before first commit") {
		t.Fatalf("expected clear unborn push error, got %#v", result)
	}

	result = waitRunError(t, repo, "git restore README.md", "")
	if !strings.Contains(result.Error, "cannot restore worktree before first commit") {
		t.Fatalf("expected clear unborn restore error, got %#v", result)
	}
}

func TestGitRunTagCreateOnUnbornBranchReturnsClearError(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "unborn-tag")
	waitRun(t, repo, "git init", "")

	result := waitRunError(t, repo, "git tag v1", "")
	if !strings.Contains(result.Error, "cannot create tag before first commit") {
		t.Fatalf("expected clear unborn tag error, got %#v", result)
	}

	result = waitRunError(t, repo, `git tag -a v2 -m "Version 2"`, "")
	if !strings.Contains(result.Error, "cannot create tag before first commit") {
		t.Fatalf("expected clear unborn annotated tag error, got %#v", result)
	}
}

func TestGitRunRejectsShellSyntax(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "repo")
	initRepository(t, repo, "README.md", "hello\n", "initial")
	waitRunError(t, repo, "git status && git push", "")
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
			t.Fatalf("%s failed: %s", command, result.Error)
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s timed out with state %s", command, result.State)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitRunError(t *testing.T, repoPath, command, options string) pollResult {
	t.Helper()
	id := StartRun(repoPath, command, options)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var result pollResult
		if err := json.Unmarshal([]byte(Poll(id)), &result); err != nil {
			t.Fatal(err)
		}
		switch result.State {
		case StateError, StateCanceled:
			Dispose(id)
			return result
		case StateDone:
			Dispose(id)
			t.Fatalf("expected %s to fail, got %#v", command, result)
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

func containsRemoteBranch(value any, remote, name string) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		data, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if data["remote"] == remote && data["name"] == name {
			return true
		}
	}
	return false
}

func containsBranchHash(value any, remote, name, hash string) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		data, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if data["remote"] == remote && data["name"] == name && data["hash"] == hash {
			return true
		}
	}
	return false
}

func containsCommitFile(value any, path, status string) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		data, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if data["path"] == path && data["status"] == status {
			return true
		}
	}
	return false
}

func assertBranchUpstream(t *testing.T, repoPath, branch, remote, merge string) {
	t.Helper()
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := repo.Config()
	if err != nil {
		t.Fatal(err)
	}
	branchConfig := cfg.Branches[branch]
	if branchConfig == nil {
		t.Fatalf("expected branch config for %s, got %#v", branch, cfg.Branches)
	}
	if branchConfig.Remote != remote || branchConfig.Merge != plumbing.ReferenceName(merge) {
		t.Fatalf("expected %s to track %s/%s, got %#v", branch, remote, merge, branchConfig)
	}
}

func containsStatus(value any, path, staging, worktree string) bool {
	items, ok := value.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		data, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if data["path"] == path && data["staging"] == staging && data["worktree"] == worktree {
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
