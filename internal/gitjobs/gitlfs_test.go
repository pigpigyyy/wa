package gitjobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
)

type lfsTestServer struct {
	server  *httptest.Server
	mu      sync.Mutex
	objects map[string][]byte
	events  []string
}

func newLFSTestServer(t *testing.T) *lfsTestServer {
	t.Helper()
	state := &lfsTestServer{objects: map[string][]byte{}}
	state.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/objects/batch":
			var batch lfsBatchRequest
			if err := json.NewDecoder(request.Body).Decode(&batch); err != nil {
				http.Error(writer, err.Error(), http.StatusBadRequest)
				return
			}
			state.record("batch:" + batch.Operation)
			response := lfsBatchResponse{}
			for _, object := range batch.Objects {
				item := object
				state.mu.Lock()
				_, exists := state.objects[object.OID]
				state.mu.Unlock()
				switch batch.Operation {
				case "upload":
					if !exists {
						item.Actions = map[string]lfsAction{
							"upload": {Href: state.server.URL + "/objects/" + object.OID},
							"verify": {Href: state.server.URL + "/verify/" + object.OID},
						}
					}
				case "download":
					if exists {
						item.Actions = map[string]lfsAction{"download": {Href: state.server.URL + "/objects/" + object.OID}}
					} else {
						item.Error = &lfsObjectError{Code: http.StatusNotFound, Message: "missing object"}
					}
				}
				response.Objects = append(response.Objects, item)
			}
			writer.Header().Set("Content-Type", "application/vnd.git-lfs+json")
			_ = json.NewEncoder(writer).Encode(response)
		case request.Method == http.MethodPost && strings.HasPrefix(request.URL.Path, "/verify/"):
			oid := strings.TrimPrefix(request.URL.Path, "/verify/")
			state.mu.Lock()
			_, exists := state.objects[oid]
			state.mu.Unlock()
			if !exists {
				http.NotFound(writer, request)
				return
			}
			state.record("verify:" + oid)
			writer.WriteHeader(http.StatusOK)
		case strings.HasPrefix(request.URL.Path, "/objects/"):
			oid := strings.TrimPrefix(request.URL.Path, "/objects/")
			switch request.Method {
			case http.MethodPut:
				data, err := io.ReadAll(request.Body)
				if err != nil {
					http.Error(writer, err.Error(), http.StatusBadRequest)
					return
				}
				state.mu.Lock()
				state.objects[oid] = data
				state.mu.Unlock()
				state.record("upload:" + oid)
				writer.WriteHeader(http.StatusOK)
			case http.MethodGet:
				state.mu.Lock()
				data, exists := state.objects[oid]
				state.mu.Unlock()
				if !exists {
					http.NotFound(writer, request)
					return
				}
				state.record("download:" + oid)
				_, _ = writer.Write(data)
			default:
				http.Error(writer, "unsupported method", http.StatusMethodNotAllowed)
			}
		default:
			http.NotFound(writer, request)
		}
	}))
	t.Cleanup(state.server.Close)
	return state
}

func (s *lfsTestServer) record(event string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *lfsTestServer) hasObject(oid string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objects[oid]
	return ok
}

func (s *lfsTestServer) hasEvent(expected string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range s.events {
		if event == expected {
			return true
		}
	}
	return false
}

func TestLFSPointerEncoding(t *testing.T) {
	data := []byte("large binary data")
	hash := sha256.Sum256(data)
	pointer := lfsPointer{OID: hex.EncodeToString(hash[:]), Size: int64(len(data))}
	encoded := encodeLFSPointer(pointer)
	decoded, ok := decodeLFSPointer(encoded)
	if !ok || decoded != pointer {
		t.Fatalf("pointer round trip failed: %#v, %q", decoded, encoded)
	}
	for _, invalid := range [][]byte{
		[]byte("not a pointer"),
		[]byte("version https://git-lfs.github.com/spec/v1\noid sha256:bad\nsize 1\n"),
		[]byte("version https://git-lfs.github.com/spec/v1\noid sha256:" + pointer.OID + "\nsize -1\n"),
	} {
		if _, ok := decodeLFSPointer(invalid); ok {
			t.Fatalf("unexpected valid pointer: %q", invalid)
		}
	}
}

func TestGitRunLFSWorkflow(t *testing.T) {
	lfsServer := newLFSTestServer(t)
	root := t.TempDir()
	remotePath := filepath.Join(root, "remote.git")
	repoPath := filepath.Join(root, "repo")
	clonePath := filepath.Join(root, "clone")
	if _, err := git.PlainInit(remotePath, true); err != nil {
		t.Fatal(err)
	}
	repo, err := git.PlainInit(repoPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{remotePath}}); err != nil {
		t.Fatal(err)
	}
	setTestLFSURL(t, repo, lfsServer.server.URL)

	writeFile(t, filepath.Join(repoPath, ".gitattributes"), "*.bin filter=lfs diff=lfs merge=lfs -text\n")
	writeFile(t, filepath.Join(repoPath, ".lfsconfig"), "[lfs]\n\turl = "+lfsServer.server.URL+"\n")
	largeData := strings.Repeat("LFS data\n", 4096)
	writeFile(t, filepath.Join(repoPath, "asset.bin"), largeData)
	addResult := resultData(t, waitRun(t, repoPath, "git add .", ""))
	if !containsString(addResult["lfs"], "asset.bin") {
		t.Fatalf("expected add result to identify LFS path, got %#v", addResult)
	}
	indexData, err := readIndexFile(repo, "asset.bin")
	if err != nil {
		t.Fatal(err)
	}
	pointer, ok := decodeLFSPointer(indexData)
	if !ok {
		t.Fatalf("expected index to contain LFS pointer, got %q", indexData)
	}
	if data, err := os.ReadFile(filepath.Join(repoPath, "asset.bin")); err != nil || string(data) != largeData {
		t.Fatalf("expected worktree to retain real LFS data: %v", err)
	}
	status := resultData(t, waitRun(t, repoPath, "git status", ""))
	if !containsStatus(status["files"].([]any), "asset.bin", "A", " ") {
		t.Fatalf("expected staged LFS file with clean worktree, got %#v", status)
	}
	initialCommit := resultData(t, waitRun(t, repoPath, `git commit -m "Add LFS asset"`, ""))["commit"].(string)
	status = resultData(t, waitRun(t, repoPath, "git status", ""))
	if clean, _ := status["clean"].(bool); !clean {
		t.Fatalf("expected clean LFS worktree after commit, got %#v", status)
	}

	push := resultData(t, waitRun(t, repoPath, "git push origin master", ""))
	if !lfsServer.hasObject(pointer.OID) {
		t.Fatalf("expected LFS object %s to be uploaded", pointer.OID)
	}
	if !lfsServer.hasEvent("verify:" + pointer.OID) {
		t.Fatalf("expected LFS object %s to be verified", pointer.OID)
	}
	if push["lfs"] == nil {
		t.Fatalf("expected push result to include LFS data: %#v", push)
	}

	waitRun(t, root, "git clone "+quoteArg(remotePath)+" clone", "")
	cloneRepo, err := git.PlainOpen(clonePath)
	if err != nil {
		t.Fatal(err)
	}
	setTestLFSURL(t, cloneRepo, lfsServer.server.URL)
	hydrated, err := os.ReadFile(filepath.Join(clonePath, "asset.bin"))
	if err != nil || string(hydrated) != largeData {
		t.Fatalf("expected clone to hydrate LFS file: %v", err)
	}
	if err := os.Remove(lfsObjectPath(clonePath, pointer.OID)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(clonePath, "asset.bin"), encodeLFSPointer(pointer), 0666); err != nil {
		t.Fatal(err)
	}
	pull := resultData(t, waitRun(t, clonePath, "git pull origin master", ""))
	if pull["lfs"] == nil {
		t.Fatalf("expected pull result to include LFS data: %#v", pull)
	}
	hydrated, err = os.ReadFile(filepath.Join(clonePath, "asset.bin"))
	if err != nil || string(hydrated) != largeData {
		t.Fatalf("expected pull to hydrate LFS file: %v", err)
	}
	status = resultData(t, waitRun(t, clonePath, "git status", ""))
	if clean, _ := status["clean"].(bool); !clean {
		t.Fatalf("expected hydrated clone to remain clean, got %#v", status)
	}
	waitRun(t, clonePath, "git checkout -b lfs-copy", "")
	waitRun(t, clonePath, "git checkout master", "")
	hydrated, err = os.ReadFile(filepath.Join(clonePath, "asset.bin"))
	if err != nil || string(hydrated) != largeData {
		t.Fatalf("expected checkout to preserve hydrated LFS file: %v", err)
	}

	writeFile(t, filepath.Join(clonePath, "asset.bin"), largeData+"changed\n")
	checkoutError := waitRunError(t, clonePath, "git checkout lfs-copy", "")
	if !strings.Contains(checkoutError.Error, "worktree contains unstaged changes") {
		t.Fatalf("expected modified LFS file to block checkout, got %#v", checkoutError)
	}
	changed, err := os.ReadFile(filepath.Join(clonePath, "asset.bin"))
	if err != nil || string(changed) != largeData+"changed\n" {
		t.Fatalf("expected rejected checkout to preserve modified LFS file: %v", err)
	}
	writeFile(t, filepath.Join(clonePath, "README.md"), "ordinary file\n")
	waitRun(t, clonePath, "git add README.md", "")
	status = resultData(t, waitRun(t, clonePath, "git status", ""))
	if !containsStatus(status["files"].([]any), "asset.bin", " ", "M") {
		t.Fatalf("expected adding another file to leave LFS change unstaged, got %#v", status)
	}
	status = resultData(t, waitRun(t, clonePath, "git status", ""))
	if !containsStatus(status["files"].([]any), "asset.bin", " ", "M") {
		t.Fatalf("expected changed LFS file to be modified, got %#v", status)
	}
	waitRun(t, clonePath, "git add asset.bin", "")
	status = resultData(t, waitRun(t, clonePath, "git status", ""))
	if !containsStatus(status["files"].([]any), "asset.bin", "M", " ") {
		t.Fatalf("expected changed LFS file to stage as pointer, got %#v", status)
	}
	waitRun(t, clonePath, "git restore --staged asset.bin", "")
	waitRun(t, clonePath, "git restore asset.bin", "")
	restored, err := os.ReadFile(filepath.Join(clonePath, "asset.bin"))
	if err != nil || string(restored) != largeData {
		t.Fatalf("expected restore to hydrate original LFS file: %v", err)
	}
	writeFile(t, filepath.Join(clonePath, "asset.bin"), largeData+"second version\n")
	waitRun(t, clonePath, "git add asset.bin", "")
	secondCommit := resultData(t, waitRun(t, clonePath, `git commit -m "Update LFS asset"`, ""))["commit"].(string)
	if secondCommit == initialCommit {
		t.Fatal("expected second LFS commit")
	}
	waitRun(t, clonePath, "git reset --hard "+initialCommit+" --confirm", "")
	resetData, err := os.ReadFile(filepath.Join(clonePath, "asset.bin"))
	if err != nil || string(resetData) != largeData {
		t.Fatalf("expected hard reset to hydrate original LFS file: %v", err)
	}
}

func setTestLFSURL(t *testing.T, repo *git.Repository, endpoint string) {
	t.Helper()
	cfg, err := repo.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Raw.Section("lfs").SetOption("url", endpoint)
	if err := repo.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
}

func containsString(value any, expected string) bool {
	items, ok := value.([]any)
	if !ok {
		if stringsList, ok := value.([]string); ok {
			for _, item := range stringsList {
				if item == expected {
					return true
				}
			}
		}
		return false
	}
	for _, item := range items {
		if fmt.Sprint(item) == expected {
			return true
		}
	}
	return false
}
