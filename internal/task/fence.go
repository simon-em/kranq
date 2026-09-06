package task

// The fence makes an effect conditional on a compare-and-swap at the git server,
// which is the one place both a partitioned attempt and its replacement can
// still reach. The host claims the fence before the VM starts; this is the other
// half, which runs inside the VM and ties the branch push to the fence in a
// single atomic push.
//
// A raw `git push` would bypass all of it, so the guard hook makes that a loud
// failure rather than a silent unfenced write.
const fencePreamble = `
kranq_fence_guard() {
    [ -d .git ] || return 0
    mkdir -p .git/hooks
    cat > .git/hooks/pre-push <<'KRANQ_HOOK'
#!/bin/sh
[ "${KRANQ_FENCE_BYPASS:-}" = "1" ] && exit 0
echo "kranq: this task declares effects.push, so pushes must go through" >&2
echo "kranq: 'kranq_push <refspec>...', which ties the push to the run's fence." >&2
echo "kranq: a plain 'git push' here could duplicate the effect of a lost run." >&2
exit 1
KRANQ_HOOK
    chmod 755 .git/hooks/pre-push
    git config --unset-all core.hooksPath 2>/dev/null || true
}

kranq_push() {
    if [ -z "${KRANQ_FENCE_REF:-}" ]; then
        echo "kranq_push: no fence was claimed for this run" >&2
        return 1
    fi
    kranq_fence_remote="$(git remote get-url --push origin)"
    kranq_fence_at="$(git ls-remote "$kranq_fence_remote" "$KRANQ_FENCE_REF" | awk 'NR==1{print $1}')"
    if [ -z "$kranq_fence_at" ]; then
        echo "kranq_push: the fence $KRANQ_FENCE_REF is gone, so another attempt may own this" >&2
        echo "kranq_push: effect now. Refusing to push." >&2
        return 1
    fi
    git fetch --quiet --force "$kranq_fence_remote" "$KRANQ_FENCE_REF:refs/kranq/held"
    case "$(git log -1 --format=%B refs/kranq/held)" in
        *"\"task\":\"$KRANQ_FENCE_TASK\""*) ;;
        *)
            echo "kranq_push: the fence is held by another run. Refusing to push." >&2
            return 1
            ;;
    esac
    kranq_fence_tree="$(git hash-object -t tree -w --stdin </dev/null)"
    kranq_fence_next="$(printf '%s' "$KRANQ_FENCE_RECORD" \
        | GIT_AUTHOR_NAME=kranq GIT_AUTHOR_EMAIL=kranq@localhost \
          GIT_COMMITTER_NAME=kranq GIT_COMMITTER_EMAIL=kranq@localhost \
          git commit-tree "$kranq_fence_tree" -p "$kranq_fence_at")"
    KRANQ_FENCE_BYPASS=1 git push --atomic \
        --force-with-lease="$KRANQ_FENCE_REF:$kranq_fence_at" \
        "$kranq_fence_remote" "$@" "$kranq_fence_next:$KRANQ_FENCE_REF"
}

kranq_fence_guard
`
