package fence

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

func (c *Client) Claim(ctx context.Context, s Scope, task string) (Claim, Holder, error) {
	h := Holder{
		Task: task, Node: c.Node, Kind: s.Kind, Repo: s.Repo, Branch: s.Branch,
		Phase: "claimed", ClaimedAt: time.Now().UTC().Truncate(time.Second),
	}
	ref := s.Ref()
	if err := c.Init(ctx); err != nil {
		return Claim{}, h, err
	}
	oid, err := c.commit(ctx, h, "")
	if err != nil {
		return Claim{}, h, err
	}
	_, stderr, err := c.git(ctx, "", "push", "--force-with-lease="+ref+":", c.URL, oid+":"+ref)
	if err != nil {
		if rejected(stderr) {
			return Claim{}, h, fmt.Errorf("%w: %s", ErrHeld, ref)
		}
		return Claim{}, h, pushError("claiming the fence", stderr, err)
	}
	return Claim{Ref: ref, OID: oid}, h, nil
}

func (c *Client) Advance(ctx context.Context, cl Claim, h Holder, phase string, refspecs ...string) (Claim, error) {
	if cl.OID == "" {
		return cl, ErrNoFence
	}
	h.Phase = phase
	oid, err := c.commit(ctx, h, cl.OID)
	if err != nil {
		return cl, err
	}
	args := append([]string{"push", "--atomic", "--force-with-lease=" + cl.Ref + ":" + cl.OID, c.URL}, refspecs...)
	args = append(args, oid+":"+cl.Ref)
	_, stderr, err := c.git(ctx, "", args...)
	if err != nil {
		switch {
		case noAtomic(stderr):
			return cl, fmt.Errorf("%w: refusing to push %v unfenced", ErrNoAtomic, refspecs)
		case rejected(stderr):
			return cl, fmt.Errorf("%w: %s", ErrBroken, cl.Ref)
		}
		return cl, pushError("advancing the fence", stderr, err)
	}
	return Claim{Ref: cl.Ref, OID: oid}, nil
}

func (c *Client) Release(ctx context.Context, cl Claim) error {
	if cl.OID == "" {
		return ErrNoFence
	}
	_, stderr, err := c.git(ctx, "", "push", "--force-with-lease="+cl.Ref+":"+cl.OID, c.URL, ":"+cl.Ref)
	if err != nil {
		if rejected(stderr) {
			return fmt.Errorf("%w: %s", ErrBroken, cl.Ref)
		}
		return pushError("releasing the fence", stderr, err)
	}
	return nil
}

func (c *Client) Break(ctx context.Context, ref, expect string) error {
	if err := c.Init(ctx); err != nil {
		return err
	}
	lease := ref + ":"
	if expect != "" {
		lease = ref + ":" + expect
	}
	_, stderr, err := c.git(ctx, "", "push", "--force-with-lease="+lease, c.URL, ":"+ref)
	if err != nil {
		if rejected(stderr) {
			return fmt.Errorf("%w: %s moved since it was read", ErrBroken, ref)
		}
		return pushError("breaking the fence", stderr, err)
	}
	return nil
}

func (c *Client) List(ctx context.Context) ([]Entry, error) {
	if err := c.Init(ctx); err != nil {
		return nil, err
	}
	out, stderr, err := c.git(ctx, "", "ls-remote", c.URL, Namespace+"/*")
	if err != nil {
		return nil, fmt.Errorf("listing fences: %w: %s", err, strings.TrimSpace(stderr))
	}
	var entries []Entry
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		oid, ref, ok := strings.Cut(strings.TrimSpace(line), "\t")
		if !ok || oid == "" {
			continue
		}
		e := Entry{Ref: ref, OID: oid}
		if h, err := c.readHolder(ctx, ref, oid); err == nil {
			e.Holder = h
		}
		entries = append(entries, e)
	}
	return entries, nil
}

func (c *Client) Show(ctx context.Context, ref string) (Entry, error) {
	if err := c.Init(ctx); err != nil {
		return Entry{}, err
	}
	out, stderr, err := c.git(ctx, "", "ls-remote", c.URL, ref)
	if err != nil {
		return Entry{}, fmt.Errorf("reading %s: %w: %s", ref, err, strings.TrimSpace(stderr))
	}
	oid, _, ok := strings.Cut(strings.TrimSpace(out), "\t")
	if !ok || oid == "" {
		return Entry{}, fmt.Errorf("%w: %s", ErrNoFence, ref)
	}
	h, err := c.readHolder(ctx, ref, oid)
	if err != nil {
		return Entry{Ref: ref, OID: oid}, err
	}
	return Entry{Ref: ref, OID: oid, Holder: h}, nil
}

func (c *Client) readHolder(ctx context.Context, ref, oid string) (Holder, error) {
	var h Holder
	local := "refs/forge/read/" + oid
	if _, stderr, err := c.git(ctx, "", "fetch", "--quiet", "--force", c.URL, ref+":"+local); err != nil {
		return h, fmt.Errorf("fetching %s: %w: %s", ref, err, strings.TrimSpace(stderr))
	}
	body, stderr, err := c.git(ctx, "", "log", "-1", "--format=%B", local)
	if err != nil {
		return h, fmt.Errorf("reading %s: %w: %s", ref, err, strings.TrimSpace(stderr))
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &h); err != nil {
		return h, fmt.Errorf("the fence record at %s is not a forge record", ref)
	}
	return h, nil
}

func rejected(stderr string) bool {
	for _, marker := range []string{"(stale info)", "[rejected]", "[remote rejected]", "(atomic push failed)"} {
		if strings.Contains(stderr, marker) {
			return true
		}
	}
	return false
}

func noAtomic(stderr string) bool {
	return strings.Contains(stderr, "does not support --atomic")
}

func pushError(what, stderr string, err error) error {
	if s := strings.TrimSpace(stderr); s != "" {
		return fmt.Errorf("%s: %w: %s", what, err, s)
	}
	return fmt.Errorf("%s: %w", what, err)
}

// Adopt finds the fence by who holds it rather than by which object it points
// at, because the job advances the fence from inside the VM and the host never
// learns the new object id. Ownership is a property of the record's content.
func (c *Client) Adopt(ctx context.Context, ref, task string) (Claim, Holder, error) {
	entry, err := c.Show(ctx, ref)
	if err != nil {
		return Claim{}, Holder{}, err
	}
	if entry.Holder.Task != task {
		return Claim{}, entry.Holder, fmt.Errorf("%w: %s is held by %s", ErrBroken, ref, describe(entry.Holder))
	}
	return Claim{Ref: ref, OID: entry.OID}, entry.Holder, nil
}

func describe(h Holder) string {
	if h.Task == "" {
		return "an unreadable record"
	}
	if h.Node == "" {
		return h.Task
	}
	return h.Task + " on " + h.Node
}
