package gitjobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/storage/memory"
)

type commandRequest struct {
	repoPath   string
	command    string
	options    runOptions
	parsed     gitCommand
	resultData map[string]any
}

type runOptions struct {
	Auth gitAuth `json:"auth"`
}

type gitAuth struct {
	Type     string `json:"type"`
	Username string `json:"username"`
	Password string `json:"password"`
	Token    string `json:"token"`
}

type gitCommand struct {
	op          string
	url         string
	paths       []string
	remote      string
	branch      string
	target      string
	message     string
	authorName  string
	authorEmail string
	action      string
	depth       int
	limit       int
	force       bool
	all         bool
	allowEmpty  bool
	amend       bool
	bare        bool
	create      bool
	delete      bool
	staged      bool
	worktree    bool
	confirm     bool
	resetMode   git.ResetMode
}

func StartRun(repoPath, command, optionsJSON string) int64 {
	repoPath = strings.TrimSpace(repoPath)
	command = strings.TrimSpace(command)
	if repoPath == "" {
		return createRejectedJob(repoPath, "run", "repo path is required")
	}
	if command == "" {
		return createRejectedJob(repoPath, "run", "git command is required")
	}
	var options runOptions
	if strings.TrimSpace(optionsJSON) != "" {
		if err := json.Unmarshal([]byte(optionsJSON), &options); err != nil {
			return createRejectedJob(repoPath, "run", fmt.Sprintf("invalid git options: %v", err))
		}
	}
	parsed, err := parseGitCommand(repoPath, command)
	if err != nil {
		return createRejectedJob(repoPath, "run", err.Error())
	}
	return startJob(parsed.op, repoPath, cloneRequest{
		path: repoPath,
		cmd: commandRequest{
			repoPath: repoPath,
			command:  command,
			options:  options,
			parsed:   parsed,
		},
	})
}

func parseGitCommand(repoPath, command string) (gitCommand, error) {
	args, err := splitGitCommand(command)
	if err != nil {
		return gitCommand{}, err
	}
	if len(args) == 0 {
		return gitCommand{}, errors.New("git command is required")
	}
	if args[0] == "git" {
		args = args[1:]
	}
	if len(args) == 0 {
		return gitCommand{}, errors.New("git subcommand is required")
	}
	if args[0] == "-C" || strings.HasPrefix(args[0], "-C") {
		return gitCommand{}, errors.New("git -C is not supported; use the repoPath argument")
	}
	switch args[0] {
	case "init":
		return parseInit(args[1:])
	case "clone":
		return parseClone(repoPath, args[1:])
	case "ls-remote":
		return parseLsRemote(args[1:])
	case "status":
		return gitCommand{op: "status"}, noExtraArgs("status", args[1:])
	case "add":
		return parseAdd(args[1:])
	case "rm":
		return parseRm(args[1:])
	case "commit":
		return parseCommit(args[1:])
	case "pull":
		return parsePull(args[1:])
	case "fetch":
		return parseFetch(args[1:])
	case "push":
		return parsePush(args[1:])
	case "log":
		return parseLog(args[1:])
	case "checkout":
		return parseCheckout(args[1:])
	case "reset":
		return parseReset(args[1:])
	case "restore":
		return parseRestore(args[1:])
	case "clean":
		return parseClean(args[1:])
	case "branch":
		return parseBranch(args[1:])
	case "tag":
		return parseTag(args[1:])
	case "remote":
		return parseRemote(args[1:])
	case "mv":
		return parseMv(args[1:])
	default:
		return gitCommand{}, fmt.Errorf("unsupported git command %q", args[0])
	}
}

func splitGitCommand(command string) ([]string, error) {
	var args []string
	var b strings.Builder
	var quote rune
	escaped := false
	for _, r := range command {
		if escaped {
			b.WriteRune(r)
			escaped = false
			continue
		}
		if quote == 0 {
			switch r {
			case '\\':
				escaped = true
			case '\'', '"':
				quote = r
			case ' ', '\t', '\n', '\r':
				if b.Len() > 0 {
					args = append(args, b.String())
					b.Reset()
				}
			case ';', '|', '>', '<', '`', '$':
				return nil, fmt.Errorf("shell syntax %q is not supported", string(r))
			case '&':
				return nil, errors.New("shell syntax is not supported")
			default:
				b.WriteRune(r)
			}
			continue
		}
		if r == quote {
			quote = 0
			continue
		}
		if r == '\\' && quote == '"' {
			escaped = true
			continue
		}
		b.WriteRune(r)
	}
	if escaped {
		return nil, errors.New("unfinished escape sequence")
	}
	if quote != 0 {
		return nil, errors.New("unterminated quoted string")
	}
	if b.Len() > 0 {
		args = append(args, b.String())
	}
	return args, nil
}

func parseInit(args []string) (gitCommand, error) {
	cmd := gitCommand{op: "init"}
	for _, arg := range args {
		switch arg {
		case "--bare":
			cmd.bare = true
		default:
			return cmd, fmt.Errorf("unsupported init option %q", arg)
		}
	}
	return cmd, nil
}

func parseClone(_ string, args []string) (gitCommand, error) {
	cmd := gitCommand{op: "clone"}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--branch", "-b":
			i++
			if i >= len(args) {
				return cmd, errors.New("clone branch value is required")
			}
			cmd.branch = args[i]
		case "--depth":
			i++
			depth, err := parsePositiveInt(args, i, "clone depth")
			if err != nil {
				return cmd, err
			}
			cmd.depth = depth
		default:
			if strings.HasPrefix(arg, "-") {
				return cmd, fmt.Errorf("unsupported clone option %q", arg)
			}
			if cmd.url == "" {
				cmd.url = arg
			} else if cmd.target == "" {
				cmd.target = arg
			} else {
				return cmd, fmt.Errorf("unexpected clone argument %q", arg)
			}
		}
	}
	if cmd.url == "" {
		return cmd, errors.New("clone URL is required")
	}
	return cmd, nil
}

func parseLsRemote(args []string) (gitCommand, error) {
	if len(args) != 1 {
		return gitCommand{}, errors.New("ls-remote requires URL")
	}
	if strings.HasPrefix(args[0], "-") {
		return gitCommand{}, fmt.Errorf("unsupported ls-remote option %q", args[0])
	}
	return gitCommand{op: "ls-remote", url: args[0]}, nil
}

func parseAdd(args []string) (gitCommand, error) {
	cmd := gitCommand{op: "add"}
	for _, arg := range args {
		switch arg {
		case "-A", "--all":
			cmd.all = true
		default:
			if strings.HasPrefix(arg, "-") {
				return cmd, fmt.Errorf("unsupported add option %q", arg)
			}
			cmd.paths = append(cmd.paths, arg)
		}
	}
	if len(cmd.paths) == 0 && !cmd.all {
		return cmd, errors.New("add path is required")
	}
	return cmd, nil
}

func parseRm(args []string) (gitCommand, error) {
	if len(args) == 0 {
		return gitCommand{}, errors.New("rm path is required")
	}
	cmd := gitCommand{op: "rm"}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return cmd, fmt.Errorf("unsupported rm option %q", arg)
		}
		cmd.paths = append(cmd.paths, arg)
	}
	return cmd, nil
}

func parseCommit(args []string) (gitCommand, error) {
	cmd := gitCommand{op: "commit"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-m", "--message":
			i++
			if i >= len(args) {
				return cmd, errors.New("commit message is required")
			}
			cmd.message = args[i]
		case "-a", "--all":
			cmd.all = true
		case "--allow-empty":
			cmd.allowEmpty = true
		case "--amend":
			cmd.amend = true
		case "--author-name":
			i++
			if i >= len(args) {
				return cmd, errors.New("author name is required")
			}
			cmd.authorName = args[i]
		case "--author-email":
			i++
			if i >= len(args) {
				return cmd, errors.New("author email is required")
			}
			cmd.authorEmail = args[i]
		default:
			return cmd, fmt.Errorf("unsupported commit option %q", args[i])
		}
	}
	if cmd.message == "" {
		return cmd, errors.New("commit message is required")
	}
	return cmd, nil
}

func parsePull(args []string) (gitCommand, error) {
	cmd := gitCommand{op: "pull", remote: "origin"}
	return parseRemoteBranchForce(cmd, args)
}

func parseFetch(args []string) (gitCommand, error) {
	cmd := gitCommand{op: "fetch", remote: "origin"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--force", "-f":
			cmd.force = true
		case "--prune", "-p":
			cmd.all = true
		case "--depth":
			i++
			depth, err := parsePositiveInt(args, i, "fetch depth")
			if err != nil {
				return cmd, err
			}
			cmd.depth = depth
		default:
			if strings.HasPrefix(args[i], "-") {
				return cmd, fmt.Errorf("unsupported fetch option %q", args[i])
			}
			if cmd.remote == "origin" {
				cmd.remote = args[i]
			} else {
				return cmd, fmt.Errorf("unexpected fetch argument %q", args[i])
			}
		}
	}
	return cmd, nil
}

func parsePush(args []string) (gitCommand, error) {
	cmd := gitCommand{op: "push", remote: "origin"}
	return parseRemoteBranchForce(cmd, args)
}

func parseRemoteBranchForce(cmd gitCommand, args []string) (gitCommand, error) {
	remoteSet := false
	for _, arg := range args {
		switch arg {
		case "--force", "-f":
			cmd.force = true
		default:
			if strings.HasPrefix(arg, "-") {
				return cmd, fmt.Errorf("unsupported %s option %q", cmd.op, arg)
			}
			if !remoteSet {
				cmd.remote = arg
				remoteSet = true
			} else if cmd.branch == "" {
				cmd.branch = arg
			} else {
				return cmd, fmt.Errorf("unexpected %s argument %q", cmd.op, arg)
			}
		}
	}
	return cmd, nil
}

func parseLog(args []string) (gitCommand, error) {
	cmd := gitCommand{op: "log", limit: 20}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--limit", "-n":
			i++
			limit, err := parsePositiveInt(args, i, "log limit")
			if err != nil {
				return cmd, err
			}
			cmd.limit = limit
		case "--":
			cmd.paths = append(cmd.paths, args[i+1:]...)
			return cmd, nil
		default:
			return cmd, fmt.Errorf("unsupported log option %q", args[i])
		}
	}
	return cmd, nil
}

func parseCheckout(args []string) (gitCommand, error) {
	cmd := gitCommand{op: "checkout"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-b":
			cmd.create = true
			i++
			if i >= len(args) {
				return cmd, errors.New("checkout branch name is required")
			}
			cmd.branch = args[i]
		case "--force", "-f":
			cmd.force = true
		default:
			if strings.HasPrefix(args[i], "-") {
				return cmd, fmt.Errorf("unsupported checkout option %q", args[i])
			}
			if cmd.target != "" {
				return cmd, fmt.Errorf("unexpected checkout argument %q", args[i])
			}
			cmd.target = args[i]
		}
	}
	if cmd.create {
		return cmd, nil
	}
	if cmd.target == "" {
		return cmd, errors.New("checkout target is required")
	}
	return cmd, nil
}

func parseReset(args []string) (gitCommand, error) {
	cmd := gitCommand{op: "reset", resetMode: git.MixedReset}
	for _, arg := range args {
		switch arg {
		case "--soft":
			cmd.resetMode = git.SoftReset
		case "--mixed":
			cmd.resetMode = git.MixedReset
		case "--hard":
			cmd.resetMode = git.HardReset
		case "--confirm":
			cmd.confirm = true
		default:
			if strings.HasPrefix(arg, "-") {
				return cmd, fmt.Errorf("unsupported reset option %q", arg)
			}
			if cmd.target != "" {
				return cmd, fmt.Errorf("unexpected reset argument %q", arg)
			}
			cmd.target = arg
		}
	}
	if cmd.target == "" {
		return cmd, errors.New("reset commit is required")
	}
	if cmd.resetMode == git.HardReset && !cmd.confirm {
		return cmd, errors.New("reset --hard requires --confirm")
	}
	return cmd, nil
}

func parseClean(args []string) (gitCommand, error) {
	cmd := gitCommand{op: "clean"}
	for _, arg := range args {
		switch arg {
		case "-f", "--force":
			cmd.force = true
		default:
			return cmd, fmt.Errorf("unsupported clean option %q", arg)
		}
	}
	if !cmd.force {
		return cmd, errors.New("clean requires -f")
	}
	return cmd, nil
}

func parseRestore(args []string) (gitCommand, error) {
	cmd := gitCommand{op: "restore"}
	for _, arg := range args {
		switch arg {
		case "--staged":
			cmd.staged = true
		case "--worktree":
			cmd.worktree = true
		default:
			if strings.HasPrefix(arg, "-") {
				return cmd, fmt.Errorf("unsupported restore option %q", arg)
			}
			if err := validateRelativeGitPath(arg); err != nil {
				return cmd, err
			}
			cmd.paths = append(cmd.paths, arg)
		}
	}
	if len(cmd.paths) == 0 {
		return cmd, errors.New("restore path is required")
	}
	if !cmd.staged && !cmd.worktree {
		cmd.worktree = true
	}
	return cmd, nil
}

func parseBranch(args []string) (gitCommand, error) {
	cmd := gitCommand{op: "branch"}
	switch len(args) {
	case 0:
		return cmd, nil
	case 2:
		if args[0] != "-d" {
			return cmd, fmt.Errorf("unsupported branch option %q", args[0])
		}
		if strings.HasPrefix(args[1], "-") {
			return cmd, errors.New("branch name is required")
		}
		cmd.branch = args[1]
		cmd.delete = true
		return cmd, nil
	case 1:
		if strings.HasPrefix(args[0], "-") {
			return cmd, fmt.Errorf("unsupported branch option %q", args[0])
		}
		cmd.branch = args[0]
		cmd.create = true
		return cmd, nil
	default:
		return cmd, fmt.Errorf("unexpected branch argument %q", args[1])
	}
}

func parseTag(args []string) (gitCommand, error) {
	cmd := gitCommand{op: "tag"}
	if len(args) == 0 {
		return cmd, nil
	}
	if len(args) == 2 {
		if args[0] != "-d" {
			return cmd, fmt.Errorf("unsupported tag option %q", args[0])
		}
		if strings.HasPrefix(args[1], "-") {
			return cmd, errors.New("tag name is required")
		}
		cmd.target = args[1]
		cmd.delete = true
		return cmd, nil
	}
	if len(args) == 1 {
		if strings.HasPrefix(args[0], "-") {
			return cmd, fmt.Errorf("unsupported tag option %q", args[0])
		}
		cmd.target = args[0]
		cmd.create = true
		return cmd, nil
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-a":
			i++
			if i >= len(args) {
				return cmd, errors.New("tag name is required")
			}
			cmd.target = args[i]
			cmd.create = true
			cmd.action = "annotated"
		case "-m", "--message":
			i++
			if i >= len(args) {
				return cmd, errors.New("tag message is required")
			}
			cmd.message = args[i]
		default:
			return cmd, fmt.Errorf("unsupported tag option %q", args[i])
		}
	}
	if cmd.action == "annotated" && cmd.message == "" {
		return cmd, errors.New("annotated tag message is required")
	}
	return cmd, nil
}

func parseRemote(args []string) (gitCommand, error) {
	cmd := gitCommand{op: "remote"}
	if len(args) == 0 {
		return cmd, nil
	}
	switch args[0] {
	case "-v":
		return cmd, noExtraArgs("remote -v", args[1:])
	case "add":
		if len(args) != 3 {
			return cmd, errors.New("remote add requires name and URL")
		}
		cmd.action = "add"
		cmd.remote = args[1]
		cmd.url = args[2]
		return cmd, nil
	case "remove":
		if len(args) != 2 {
			return cmd, errors.New("remote remove requires name")
		}
		cmd.action = "remove"
		cmd.remote = args[1]
		return cmd, nil
	case "set-url":
		if len(args) != 3 {
			return cmd, errors.New("remote set-url requires name and URL")
		}
		cmd.action = "set-url"
		cmd.remote = args[1]
		cmd.url = args[2]
		return cmd, nil
	default:
		return cmd, fmt.Errorf("unsupported remote command %q", args[0])
	}
}

func parseMv(args []string) (gitCommand, error) {
	if len(args) != 2 {
		return gitCommand{}, errors.New("mv requires source and destination paths")
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			return gitCommand{}, fmt.Errorf("unsupported mv option %q", arg)
		}
		if err := validateRelativeGitPath(arg); err != nil {
			return gitCommand{}, err
		}
	}
	return gitCommand{op: "mv", paths: []string{args[0]}, target: args[1]}, nil
}

func noExtraArgs(name string, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("%s does not accept arguments", name)
	}
	return nil
}

func parsePositiveInt(args []string, index int, label string) (int, error) {
	if index >= len(args) {
		return 0, fmt.Errorf("%s value is required", label)
	}
	value, err := strconv.Atoi(args[index])
	if err != nil || value < 1 {
		return 0, fmt.Errorf("%s must be a positive integer", label)
	}
	return value, nil
}

func runCommand(ctx context.Context, j *job) {
	cmd := j.req.cmd.parsed
	j.setRunning("running git " + cmd.op)
	var data map[string]any
	var err error
	switch cmd.op {
	case "init":
		data, err = execInit(j.req.cmd.repoPath, cmd)
	case "clone":
		data, err = execClone(ctx, j, cmd)
	case "ls-remote":
		data, err = execLsRemote(j, cmd)
	case "status":
		data, err = execStatus(j.req.cmd.repoPath)
	case "add":
		data, err = execAdd(j.req.cmd.repoPath, cmd)
	case "rm":
		data, err = execRm(j.req.cmd.repoPath, cmd)
	case "commit":
		data, err = execCommit(j.req.cmd.repoPath, cmd)
	case "pull":
		data, err = execPull(ctx, j, cmd)
	case "fetch":
		data, err = execFetch(ctx, j, cmd)
	case "push":
		data, err = execPush(ctx, j, cmd)
	case "log":
		data, err = execLog(j.req.cmd.repoPath, cmd)
	case "checkout":
		data, err = execCheckout(j.req.cmd.repoPath, cmd)
	case "reset":
		data, err = execReset(j.req.cmd.repoPath, cmd)
	case "restore":
		data, err = execRestore(j.req.cmd.repoPath, cmd)
	case "clean":
		data, err = execClean(j.req.cmd.repoPath)
	case "branch":
		data, err = execBranch(j.req.cmd.repoPath, cmd)
	case "tag":
		data, err = execTag(j.req.cmd.repoPath, cmd)
	case "remote":
		data, err = execRemote(j.req.cmd.repoPath, cmd)
	case "mv":
		data, err = execMv(j.req.cmd.repoPath, cmd)
	default:
		err = fmt.Errorf("unsupported git command %q", cmd.op)
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			j.setCanceled()
			return
		}
		j.setError(err)
		return
	}
	j.setResult(data)
	j.setDone("git " + cmd.op + " completed")
}

func execClone(ctx context.Context, j *job, cmd gitCommand) (map[string]any, error) {
	targetPath, err := cloneTargetPath(j.req.cmd.repoPath, cmd.url, cmd.target)
	if err != nil {
		return nil, err
	}
	if err := prepareClonePath(targetPath); err != nil {
		return nil, err
	}
	opts := &git.CloneOptions{
		URL:      cmd.url,
		Depth:    cmd.depth,
		Progress: progressWriter{job: j},
		Auth:     authMethod(j.req.cmd.options),
	}
	if cmd.branch != "" {
		opts.ReferenceName = plumbingBranch(cmd.branch)
		opts.SingleBranch = true
	}
	repo, err := git.PlainCloneContext(ctx, targetPath, false, opts)
	if err != nil {
		return nil, err
	}
	head, _ := repo.Head()
	data := hashData(head)
	if data == nil {
		data = map[string]any{}
	}
	data["path"] = targetPath
	return data, nil
}

func execInit(repoPath string, cmd gitCommand) (map[string]any, error) {
	repo, err := git.PlainInit(repoPath, cmd.bare)
	if err != nil {
		return nil, err
	}
	data := map[string]any{"path": repoPath, "bare": cmd.bare}
	if head, err := repo.Head(); err == nil {
		data["ref"] = head.Name().String()
	}
	return data, nil
}

func execLsRemote(j *job, cmd gitCommand) (map[string]any, error) {
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: "origin",
		URLs: []string{cmd.url},
	})
	refs, err := remote.ListContext(j.ctx, &git.ListOptions{
		Auth: authMethod(j.req.cmd.options),
	})
	if err != nil {
		return nil, err
	}
	data := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		item := map[string]any{
			"name": ref.Name().String(),
			"type": refKind(ref.Name()),
		}
		if !ref.Hash().IsZero() {
			item["hash"] = ref.Hash().String()
		}
		if ref.Target() != "" {
			item["target"] = ref.Target().String()
		}
		data = append(data, item)
	}
	return map[string]any{"url": cmd.url, "refs": data}, nil
}

func execStatus(repoPath string) (map[string]any, error) {
	_, worktree, err := openWorktree(repoPath)
	if err != nil {
		return nil, err
	}
	status, err := worktree.Status()
	if err != nil {
		return nil, err
	}
	files := make([]map[string]string, 0, len(status))
	for path, file := range status {
		if file.Staging == git.Unmodified && file.Worktree == git.Unmodified {
			continue
		}
		files = append(files, map[string]string{
			"path":     path,
			"staging":  string(file.Staging),
			"worktree": string(file.Worktree),
		})
	}
	return map[string]any{"clean": status.IsClean(), "files": files}, nil
}

func execAdd(repoPath string, cmd gitCommand) (map[string]any, error) {
	_, worktree, err := openWorktree(repoPath)
	if err != nil {
		return nil, err
	}
	if cmd.all {
		if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
			return nil, err
		}
		return map[string]any{"paths": []string{"."}}, nil
	}
	for _, p := range cmd.paths {
		if p == "." {
			if err := worktree.AddWithOptions(&git.AddOptions{All: true}); err != nil {
				return nil, err
			}
			continue
		}
		if hasGlob(p) {
			if err := worktree.AddGlob(p); err != nil {
				return nil, err
			}
			continue
		}
		if _, err := worktree.Add(p); err != nil {
			return nil, err
		}
	}
	return map[string]any{"paths": cmd.paths}, nil
}

func execRm(repoPath string, cmd gitCommand) (map[string]any, error) {
	_, worktree, err := openWorktree(repoPath)
	if err != nil {
		return nil, err
	}
	for _, p := range cmd.paths {
		if hasGlob(p) {
			if err := worktree.RemoveGlob(p); err != nil {
				return nil, err
			}
			continue
		}
		if _, err := worktree.Remove(p); err != nil {
			return nil, err
		}
	}
	return map[string]any{"paths": cmd.paths}, nil
}

func execCommit(repoPath string, cmd gitCommand) (map[string]any, error) {
	_, worktree, err := openWorktree(repoPath)
	if err != nil {
		return nil, err
	}
	author := &object.Signature{
		Name:  firstNonEmpty(cmd.authorName, "Dora"),
		Email: firstNonEmpty(cmd.authorEmail, "dora@example.com"),
		When:  time.Now(),
	}
	hash, err := worktree.Commit(cmd.message, &git.CommitOptions{
		All:               cmd.all,
		AllowEmptyCommits: cmd.allowEmpty,
		Amend:             cmd.amend,
		Author:            author,
		Committer:         author,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"commit": hash.String()}, nil
}

func execPull(ctx context.Context, j *job, cmd gitCommand) (map[string]any, error) {
	_, worktree, err := openWorktree(j.req.cmd.repoPath)
	if err != nil {
		return nil, err
	}
	opts := &git.PullOptions{
		RemoteName: cmd.remote,
		Force:      cmd.force,
		Progress:   progressWriter{job: j},
		Auth:       authMethod(j.req.cmd.options),
	}
	if cmd.branch != "" {
		opts.ReferenceName = plumbingBranch(cmd.branch)
		opts.SingleBranch = true
	}
	err = worktree.PullContext(ctx, opts)
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		return map[string]any{"upToDate": true}, nil
	}
	return nil, err
}

func execFetch(ctx context.Context, j *job, cmd gitCommand) (map[string]any, error) {
	repo, err := git.PlainOpen(j.req.cmd.repoPath)
	if err != nil {
		return nil, err
	}
	err = repo.FetchContext(ctx, &git.FetchOptions{
		RemoteName: cmd.remote,
		Depth:      cmd.depth,
		Force:      cmd.force,
		Prune:      cmd.all,
		Progress:   progressWriter{job: j},
		Auth:       authMethod(j.req.cmd.options),
	})
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		return map[string]any{"upToDate": true}, nil
	}
	return nil, err
}

func execPush(ctx context.Context, j *job, cmd gitCommand) (map[string]any, error) {
	repo, err := git.PlainOpen(j.req.cmd.repoPath)
	if err != nil {
		return nil, err
	}
	opts := &git.PushOptions{
		RemoteName: cmd.remote,
		Force:      cmd.force,
		Progress:   progressWriter{job: j},
		Auth:       authMethod(j.req.cmd.options),
	}
	if cmd.branch != "" {
		ref := plumbingBranch(cmd.branch)
		opts.RefSpecs = []config.RefSpec{config.RefSpec(ref + ":" + ref)}
	}
	err = repo.PushContext(ctx, opts)
	if errors.Is(err, git.NoErrAlreadyUpToDate) {
		return map[string]any{"upToDate": true}, nil
	}
	return nil, err
}

func execLog(repoPath string, cmd gitCommand) (map[string]any, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, err
	}
	opts := &git.LogOptions{Order: git.LogOrderCommitterTime}
	if len(cmd.paths) != 0 {
		path := cmd.paths[0]
		opts.FileName = &path
	}
	iter, err := repo.Log(opts)
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var commits []map[string]any
	limit := cmd.limit
	err = iter.ForEach(func(c *object.Commit) error {
		if limit <= 0 {
			return storerStop
		}
		commits = append(commits, map[string]any{
			"hash":    c.Hash.String(),
			"message": strings.TrimSpace(c.Message),
			"author":  c.Author.Name,
			"email":   c.Author.Email,
			"when":    c.Author.When.Format(time.RFC3339),
		})
		limit--
		return nil
	})
	if errors.Is(err, storerStop) {
		err = nil
	}
	return map[string]any{"commits": commits}, err
}

var storerStop = errors.New("stop commit iteration")

func execCheckout(repoPath string, cmd gitCommand) (map[string]any, error) {
	repo, worktree, err := openWorktree(repoPath)
	if err != nil {
		return nil, err
	}
	opts := &git.CheckoutOptions{Create: cmd.create, Force: cmd.force}
	if cmd.create {
		opts.Branch = plumbingBranch(cmd.branch)
	} else if hash, ok := parseHash(cmd.target); ok {
		opts.Hash = hash
	} else {
		opts.Branch = plumbingBranch(cmd.target)
	}
	if !opts.Hash.IsZero() {
		if _, err := repo.CommitObject(opts.Hash); err != nil {
			return nil, err
		}
	}
	if err := worktree.Checkout(opts); err != nil {
		return nil, err
	}
	head, _ := repo.Head()
	return hashData(head), nil
}

func execReset(repoPath string, cmd gitCommand) (map[string]any, error) {
	repo, worktree, err := openWorktree(repoPath)
	if err != nil {
		return nil, err
	}
	hash, ok := parseHash(cmd.target)
	if !ok {
		return nil, errors.New("reset target must be a commit hash")
	}
	if err := worktree.Reset(&git.ResetOptions{Commit: hash, Mode: cmd.resetMode}); err != nil {
		return nil, err
	}
	head, _ := repo.Head()
	return hashData(head), nil
}

func execRestore(repoPath string, cmd gitCommand) (map[string]any, error) {
	repo, worktree, err := openWorktree(repoPath)
	if err != nil {
		return nil, err
	}
	if cmd.staged {
		if err := worktree.Restore(&git.RestoreOptions{
			Staged:   true,
			Worktree: cmd.worktree,
			Files:    cmd.paths,
		}); err != nil {
			return nil, err
		}
		return map[string]any{"paths": cmd.paths, "staged": true, "worktree": cmd.worktree}, nil
	}
	head, err := repo.Head()
	if err != nil {
		return nil, err
	}
	commit, err := repo.CommitObject(head.Hash())
	if err != nil {
		return nil, err
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}
	for _, path := range cmd.paths {
		if err := restoreWorktreePath(repoPath, tree, path); err != nil {
			return nil, err
		}
	}
	return map[string]any{"paths": cmd.paths, "staged": false, "worktree": true}, nil
}

func execClean(repoPath string) (map[string]any, error) {
	_, worktree, err := openWorktree(repoPath)
	if err != nil {
		return nil, err
	}
	return nil, worktree.Clean(&git.CleanOptions{Dir: true})
}

func execBranch(repoPath string, cmd gitCommand) (map[string]any, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, err
	}
	if cmd.create {
		head, err := repo.Head()
		if err != nil {
			return nil, err
		}
		refName := plumbing.NewBranchReferenceName(cmd.branch)
		if _, err := repo.Reference(refName, false); err == nil {
			return nil, git.ErrBranchExists
		} else if !errors.Is(err, plumbing.ErrReferenceNotFound) {
			return nil, err
		}
		ref := plumbing.NewHashReference(refName, head.Hash())
		if err := repo.Storer.SetReference(ref); err != nil {
			return nil, err
		}
		return map[string]any{"branch": cmd.branch, "hash": head.Hash().String()}, nil
	}
	if cmd.delete {
		refName := plumbing.NewBranchReferenceName(cmd.branch)
		if _, err := repo.Reference(refName, false); err != nil {
			return nil, err
		}
		if err := repo.Storer.RemoveReference(refName); err != nil {
			return nil, err
		}
		if err := repo.DeleteBranch(cmd.branch); err != nil && !errors.Is(err, git.ErrBranchNotFound) {
			return nil, err
		}
		return map[string]any{"branch": cmd.branch, "deleted": true}, nil
	}

	head, _ := repo.Head()
	current := ""
	if head != nil && head.Name().IsBranch() {
		current = head.Name().Short()
	}
	iter, err := repo.Branches()
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	branches := []map[string]any{}
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().Short()
		branches = append(branches, map[string]any{
			"name":    name,
			"hash":    ref.Hash().String(),
			"current": name == current,
		})
		return nil
	})
	return map[string]any{"branches": branches, "current": current}, err
}

func execTag(repoPath string, cmd gitCommand) (map[string]any, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, err
	}
	if cmd.delete {
		if err := repo.DeleteTag(cmd.target); err != nil {
			return nil, err
		}
		return map[string]any{"tag": cmd.target, "deleted": true}, nil
	}
	if cmd.create {
		head, err := repo.Head()
		if err != nil {
			return nil, err
		}
		var opts *git.CreateTagOptions
		if cmd.action == "annotated" {
			opts = &git.CreateTagOptions{
				Tagger: &object.Signature{
					Name:  firstNonEmpty(cmd.authorName, "Dora"),
					Email: firstNonEmpty(cmd.authorEmail, "dora@example.com"),
					When:  time.Now(),
				},
				Message: cmd.message,
			}
		}
		ref, err := repo.CreateTag(cmd.target, head.Hash(), opts)
		if err != nil {
			return nil, err
		}
		return map[string]any{"tag": cmd.target, "hash": ref.Hash().String(), "annotated": cmd.action == "annotated"}, nil
	}

	iter, err := repo.Tags()
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	tags := []map[string]any{}
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		tags = append(tags, map[string]any{
			"name": ref.Name().Short(),
			"hash": ref.Hash().String(),
		})
		return nil
	})
	return map[string]any{"tags": tags}, err
}

func execRemote(repoPath string, cmd gitCommand) (map[string]any, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, err
	}
	switch cmd.action {
	case "add":
		if _, err := repo.CreateRemote(&config.RemoteConfig{Name: cmd.remote, URLs: []string{cmd.url}}); err != nil {
			return nil, err
		}
		return map[string]any{"remote": cmd.remote, "urls": []string{cmd.url}}, nil
	case "remove":
		if err := repo.DeleteRemote(cmd.remote); err != nil {
			return nil, err
		}
		return map[string]any{"remote": cmd.remote, "removed": true}, nil
	case "set-url":
		cfg, err := repo.Config()
		if err != nil {
			return nil, err
		}
		remote := cfg.Remotes[cmd.remote]
		if remote == nil {
			return nil, git.ErrRemoteNotFound
		}
		remote.URLs = []string{cmd.url}
		if err := remote.Validate(); err != nil {
			return nil, err
		}
		if err := repo.SetConfig(cfg); err != nil {
			return nil, err
		}
		return map[string]any{"remote": cmd.remote, "urls": []string{cmd.url}}, nil
	}

	remotes, err := repo.Remotes()
	if err != nil {
		return nil, err
	}
	data := []map[string]any{}
	for _, remote := range remotes {
		cfg := remote.Config()
		data = append(data, map[string]any{
			"name": cfg.Name,
			"urls": append([]string(nil), cfg.URLs...),
		})
	}
	return map[string]any{"remotes": data}, nil
}

func execMv(repoPath string, cmd gitCommand) (map[string]any, error) {
	_, worktree, err := openWorktree(repoPath)
	if err != nil {
		return nil, err
	}
	from := cmd.paths[0]
	to := cmd.target
	if err := rejectDirectoryPath(repoPath, from, "mv source"); err != nil {
		return nil, err
	}
	hash, err := worktree.Move(from, to)
	if err != nil {
		return nil, err
	}
	return map[string]any{"from": from, "to": to, "hash": hash.String()}, nil
}

func openWorktree(repoPath string) (*git.Repository, *git.Worktree, error) {
	repo, err := git.PlainOpen(repoPath)
	if err != nil {
		return nil, nil, err
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return nil, nil, err
	}
	return repo, worktree, nil
}

func authMethod(options runOptions) transport.AuthMethod {
	auth := options.Auth
	switch strings.ToLower(auth.Type) {
	case "basic":
		if auth.Username == "" && auth.Password == "" {
			return nil
		}
		return &http.BasicAuth{Username: auth.Username, Password: auth.Password}
	case "token":
		if auth.Token == "" {
			return nil
		}
		return &http.BasicAuth{Username: firstNonEmpty(auth.Username, "token"), Password: auth.Token}
	default:
		return nil
	}
}

func parseHash(value string) (plumbing.Hash, bool) {
	if len(value) != 40 {
		return plumbing.ZeroHash, false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return plumbing.ZeroHash, false
		}
	}
	return plumbing.NewHash(value), true
}

func plumbingBranch(branch string) plumbing.ReferenceName {
	if strings.HasPrefix(branch, "refs/") {
		return plumbing.ReferenceName(branch)
	}
	return plumbing.ReferenceName("refs/heads/" + branch)
}

func hashData(ref *plumbing.Reference) map[string]any {
	if ref == nil {
		return nil
	}
	return map[string]any{"head": ref.Hash().String(), "ref": ref.Name().String()}
}

func hasGlob(path string) bool {
	return strings.ContainsAny(path, "*?[")
}

func validateRelativeGitPath(path string) error {
	if path == "" {
		return errors.New("path is required")
	}
	clean := filepath.Clean(path)
	if filepath.IsAbs(path) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path must be relative to the repository: %q", path)
	}
	return nil
}

func rejectDirectoryPath(repoPath, path, label string) error {
	info, err := filepath.Abs(filepath.Join(repoPath, filepath.Clean(path)))
	if err != nil {
		return err
	}
	root, err := filepath.Abs(repoPath)
	if err != nil {
		return err
	}
	if info != root && !strings.HasPrefix(info, root+string(filepath.Separator)) {
		return fmt.Errorf("%s must stay inside the repository", label)
	}
	fileInfo, err := os.Lstat(info)
	if err != nil {
		return err
	}
	if fileInfo.IsDir() {
		return fmt.Errorf("%s must be a single file", label)
	}
	return nil
}

func restoreWorktreePath(repoPath string, tree *object.Tree, path string) error {
	file, err := tree.File(path)
	if err != nil {
		return err
	}
	reader, err := file.Reader()
	if err != nil {
		return err
	}
	defer reader.Close()
	target := filepath.Join(repoPath, filepath.Clean(path))
	if err := os.MkdirAll(filepath.Dir(target), 0777); err != nil {
		return err
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0666)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, reader)
	return err
}

func refKind(name plumbing.ReferenceName) string {
	switch {
	case name == plumbing.HEAD:
		return "head"
	case name.IsBranch():
		return "branch"
	case name.IsTag():
		return "tag"
	case name.IsRemote():
		return "remote"
	default:
		return "ref"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func cloneTargetPath(parentPath, url, dir string) (string, error) {
	parentPath = strings.TrimSpace(parentPath)
	dir = strings.TrimSpace(dir)
	if parentPath == "" {
		return "", errors.New("clone parent path is required")
	}
	if dir == "" {
		dir = repoNameFromURL(url)
		if dir == "" {
			return "", errors.New("clone directory could not be inferred from URL")
		}
	}
	if filepath.IsAbs(dir) || dir != filepath.Base(dir) || dir == "." || dir == ".." {
		return "", errors.New("clone directory must be a folder name")
	}
	return filepath.Join(parentPath, dir), nil
}

func repoNameFromURL(url string) string {
	url = strings.TrimSpace(url)
	url = strings.TrimRight(url, "/")
	if url == "" {
		return ""
	}
	if i := strings.LastIndex(url, ":"); i >= 0 && !strings.Contains(url[i+1:], "/") {
		url = url[i+1:]
	}
	name := filepath.Base(url)
	name = strings.TrimSuffix(name, ".git")
	if name == "." || name == "/" {
		return ""
	}
	return name
}
