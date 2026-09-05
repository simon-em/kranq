package fence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strings"
	"time"
)

const Namespace = "refs/forge/fence"

var (
	ErrHeld     = errors.New("the fence is already held by another attempt")
	ErrBroken   = errors.New("the fence moved out from under this attempt")
	ErrNoAtomic = errors.New("the git server does not support atomic pushes")
	ErrNoFence  = errors.New("no fence is held")
)

type Scope struct {
	Kind   string
	Repo   string
	Branch string
}

func (s Scope) Ref() string {
	sum := sha256.Sum256([]byte(s.Kind + "\x00" + s.Repo + "\x00" + s.Branch))
	return Namespace + "/" + hex.EncodeToString(sum[:8])
}

type Holder struct {
	Task      string    `json:"task"`
	Node      string    `json:"node"`
	Kind      string    `json:"kind"`
	Repo      string    `json:"repo"`
	Branch    string    `json:"branch"`
	Phase     string    `json:"phase"`
	ClaimedAt time.Time `json:"claimed_at"`
}

type Claim struct {
	Ref string
	OID string
}

type Entry struct {
	Ref    string
	OID    string
	Holder Holder
}

type Runner func(ctx context.Context, dir string, env, args []string, stdin string) (stdout, stderr string, err error)

type Client struct {
	Dir   string
	URL   string
	Token string
	Node  string
	Run   Runner
}

func (c *Client) git(ctx context.Context, stdin string, args ...string) (string, string, error) {
	run := c.Run
	if run == nil {
		run = execGit
	}
	env := []string{
		"GIT_AUTHOR_NAME=forge", "GIT_AUTHOR_EMAIL=forge@localhost",
		"GIT_COMMITTER_NAME=forge", "GIT_COMMITTER_EMAIL=forge@localhost",
		"GIT_TERMINAL_PROMPT=0",
	}
	if c.Token != "" {
		env = append(env, "FORGE_GIT_TOKEN="+c.Token)
		args = append([]string{
			"-c", "credential.helper=",
			"-c", `credential.helper=!f() { echo username=x-token-auth; echo "password=$FORGE_GIT_TOKEN"; }; f`,
		}, args...)
	}
	return run(ctx, c.Dir, env, args, stdin)
}

func execGit(ctx context.Context, dir string, env, args []string, stdin string) (string, string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

func (c *Client) Init(ctx context.Context) error {
	if _, err := os.Stat(path.Join(c.Dir, "HEAD")); err == nil {
		return nil
	}
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	if _, stderr, err := c.git(ctx, "", "init", "--bare", "--quiet", c.Dir); err != nil {
		return fmt.Errorf("preparing the fence repository: %w: %s", err, stderr)
	}
	return nil
}

func (c *Client) commit(ctx context.Context, h Holder, parent string) (string, error) {
	tree, stderr, err := c.git(ctx, "", "hash-object", "-t", "tree", "-w", "--stdin")
	if err != nil {
		return "", fmt.Errorf("writing the empty tree: %w: %s", err, stderr)
	}
	body, err := json.Marshal(h)
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", strings.TrimSpace(tree), "-m", string(body)}
	if parent != "" {
		args = append(args, "-p", parent)
	}
	oid, stderr, err := c.git(ctx, "", args...)
	if err != nil {
		return "", fmt.Errorf("writing the fence record: %w: %s", err, stderr)
	}
	return strings.TrimSpace(oid), nil
}
