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
forge_fence_guard() {
    [ -d .git ] || return 0
    mkdir -p .git/hooks
    cat > .git/hooks/pre-push <<'FORGE_HOOK'
#!/bin/sh
[ "${FORGE_FENCE_BYPASS:-}" = "1" ] && exit 0
echo "forge: this task declares effects.push, so pushes must go through" >&2
echo "forge: 'forge_push <refspec>...', which ties the push to the run's fence." >&2
echo "forge: a plain 'git push' here could duplicate the effect of a lost run." >&2
exit 1
FORGE_HOOK
    chmod 755 .git/hooks/pre-push
    git config --unset-all core.hooksPath 2>/dev/null || true
}

forge_push() {
    if [ -z "${FORGE_FENCE_REF:-}" ]; then
        echo "forge_push: no fence was claimed for this run" >&2
        return 1
    fi
    forge_fence_remote="$(git remote get-url --push origin)"
    forge_fence_at="$(git ls-remote "$forge_fence_remote" "$FORGE_FENCE_REF" | awk 'NR==1{print $1}')"
    if [ -z "$forge_fence_at" ]; then
        echo "forge_push: the fence $FORGE_FENCE_REF is gone, so another attempt may own this" >&2
        echo "forge_push: effect now. Refusing to push." >&2
        return 1
    fi
    git fetch --quiet --force "$forge_fence_remote" "$FORGE_FENCE_REF:refs/forge/held"
    case "$(git log -1 --format=%B refs/forge/held)" in
        *"\"task\":\"$FORGE_FENCE_TASK\""*) ;;
        *)
            echo "forge_push: the fence is held by another run. Refusing to push." >&2
            return 1
            ;;
    esac
    forge_fence_tree="$(git hash-object -t tree -w --stdin </dev/null)"
    forge_fence_next="$(printf '%s' "$FORGE_FENCE_RECORD" \
        | GIT_AUTHOR_NAME=forge GIT_AUTHOR_EMAIL=forge@localhost \
          GIT_COMMITTER_NAME=forge GIT_COMMITTER_EMAIL=forge@localhost \
          git commit-tree "$forge_fence_tree" -p "$forge_fence_at")"
    FORGE_FENCE_BYPASS=1 git push --atomic \
        --force-with-lease="$FORGE_FENCE_REF:$forge_fence_at" \
        "$forge_fence_remote" "$@" "$forge_fence_next:$FORGE_FENCE_REF"
}

forge_fence_guard
`
