# Homebrew formula for forge. This repository is its own tap:
#
#   brew tap simontlbt/forge https://github.com/simontlbt/forge
#   brew install simontlbt/forge/forge
#
# See docs/homebrew.md. To cut a release: tag it, push the tag, then put the
# tarball's sha256 here and push that. The formula on main always points at the
# last tag, never at main itself.
class Forge < Formula
  desc "Runs CI jobs in disposable Lima VMs on a macOS build machine"
  homepage "https://github.com/simontlbt/forge"
  url "https://github.com/simontlbt/forge/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "a07c649ccbf111aa18a1fad068e9c265aa9c578b28906a6a9899129ab9095f00"
  # No license line: the repository carries no LICENSE file, and naming one here
  # would assert something untrue. Add a LICENSE and then say so.

  head "https://github.com/simontlbt/forge.git", branch: "main"

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

      Upgrade with `brew upgrade simontlbt/forge/forge`, not `forge upgrade`:
      forge refuses to replace a binary a package manager owns, because the next
      brew upgrade would undo it. Use the full name -- homebrew/core has an
      unrelated `forge` (arrayfire's visualization library) that the short name
      can reach instead.
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
