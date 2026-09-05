package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/effetmonstre/forge/internal/fence"
)

type FencePlan struct {
	Dir   string
	Node  string
	Scope fence.Scope
}

type heldFence struct {
	client *fence.Client
	claim  fence.Claim
	holder fence.Holder
}

func (e *Engine) claimFence(ctx context.Context, req Request, remote Remote, out io.Writer) (*heldFence, error) {
	if req.Fence == nil {
		return nil, nil
	}
	c := &fence.Client{
		Dir: req.Fence.Dir, URL: remote.URL(), Token: remote.Token, Node: req.Fence.Node,
	}
	claim, holder, err := c.Claim(ctx, req.Fence.Scope, req.TaskID)
	if err != nil {
		if errors.Is(err, fence.ErrHeld) {
			if entry, showErr := c.Show(ctx, req.Fence.Scope.Ref()); showErr == nil {
				return nil, fmt.Errorf("%w, by %s since %s; `forge fence show %s` for the record, "+
					"`forge fence break %s` once you have confirmed it is not still running",
					fence.ErrHeld, entry.Holder.Task, entry.Holder.ClaimedAt.Format("2006-01-02 15:04 MST"),
					entry.Ref, entry.Ref)
			}
		}
		return nil, err
	}
	fmt.Fprintf(out, "fence %s claimed\n", claim.Ref)
	return &heldFence{client: c, claim: claim, holder: holder}, nil
}

// The fence is released when nothing landed, or when the run finished cleanly.
// It is deliberately left standing when an effect landed and the run then
// failed, because a fence held by mistake costs one human command and a fence
// released by mistake costs a second pull request.
func (e *Engine) settleFence(ctx context.Context, h *heldFence, res *Result, out io.Writer) {
	if h == nil {
		return
	}
	res.FenceRef = h.claim.Ref
	current, _, err := h.client.Adopt(ctx, h.claim.Ref, h.holder.Task)
	if err != nil {
		res.FenceHeld = true
		fmt.Fprintf(out, "warning: this run no longer owns %s: %v\n", h.claim.Ref, err)
		return
	}
	pushed := current.OID != h.claim.OID
	if pushed && res.ExitCode != 0 {
		res.FenceHeld = true
		fmt.Fprintf(out, "this run pushed something and then failed, so %s is being held. "+
			"Check what landed, then `forge fence break %s` to let another run take it.\n",
			h.claim.Ref, h.claim.Ref)
		return
	}
	if err := h.client.Release(ctx, current); err != nil {
		res.FenceHeld = true
		fmt.Fprintf(out, "warning: could not release %s: %v\n", h.claim.Ref, err)
		return
	}
	fmt.Fprintf(out, "fence %s released\n", h.claim.Ref)
}

func (h *heldFence) env(taskID string) map[string]string {
	record := h.holder
	record.Phase = "pushed"
	body, err := json.Marshal(record)
	if err != nil {
		return nil
	}
	return map[string]string{
		"FORGE_FENCE_REF":    h.claim.Ref,
		"FORGE_FENCE_TASK":   taskID,
		"FORGE_FENCE_RECORD": string(body),
	}
}
