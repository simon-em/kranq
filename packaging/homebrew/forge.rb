# Homebrew formula for forge.
#
# It builds from source rather than downloading a release, so nothing has to be
# hosted anywhere and a private repository works with the ssh key you already
# push with.
#
# Copy this into a tap as Formula/forge.rb, set FORGE_REPO below, and tag a
# release. See docs/homebrew.md.
class Forge < Formula
  desc "Runs CI jobs in disposable Lima VMs on a macOS build machine"
  homepage "https://bitbucket.org/effetmonstre/forge"

  # An ssh url so a private repository needs no token: git uses the key that is
  # already set up. Switch to https if the repository is public.
  # revision: must be the COMMIT the tag points at, not the tag object. For an
  # annotated tag `git rev-parse v0.1.0` gives the tag object and brew refuses
  # the download with "tag should be X but is actually Y". Use:
  #   git rev-parse v0.1.0^{commit}
  url "git@bitbucket.org:effetmonstre/forge.git",
      using:    :git,
      tag:      "v0.1.0",
      revision: "0000000000000000000000000000000000000000"
  version "0.1.0"
  license "UNLICENSED"

  head "git@bitbucket.org:effetmonstre/forge.git", using: :git, branch: "main"

  depends_on "go" => :build
  depends_on :macos

  def install
    # std_go_args already passes -s -w; adding them again shows up duplicated in
    # the build line for no benefit.
    ldflags = %W[
      -X github.com/effetmonstre/forge/internal/cli.Version=#{version}
    ]
    system "go", "build", *std_go_args(ldflags: ldflags)
  end

  def caveats
    <<~EOS
      brew installed the binary. Everything else forge needs is installed by
      forge itself, on purpose: it keeps its own Lima under ~/.forge/deps and
      calls it by absolute path, so a Homebrew lima appearing or disappearing
      cannot change what runs.

        forge install --deps-only               # fetch and verify lima
        forge install --deps-only --with-daemon # and run the daemon at login
        forge doctor                            # check this machine

      Upgrade with `brew upgrade forge`, not `forge upgrade`: forge refuses to
      replace a binary a package manager owns, because the next brew upgrade
      would undo it.
    EOS
  end

  service do
    run [opt_bin/"forge", "daemon", "run"]
    keep_alive successful_exit: false
    log_path var/"log/forge.log"
    error_log_path var/"log/forge.log"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/forge version")

    (testpath/"spec.yaml").write <<~YAML
      name: brew-test
      steps:
        - name: it compiles
          run: echo hello
    YAML
    # validate and render need no daemon, no VM and no network, so they are the
    # right things for a sandboxed test to prove the binary works.
    system bin/"forge", "validate", testpath/"spec.yaml"
    assert_match "echo hello", shell_output("#{bin}/forge render #{testpath}/spec.yaml")
  end
end
