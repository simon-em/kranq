# Homebrew formula for kranq. This repository is its own tap:
#
#   brew tap simon-em/kranq https://github.com/simon-em/kranq
#   brew install simon-em/kranq/kranq
#
# See docs/homebrew.md. To cut a release: tag it, push the tag, then put the
# tarball's sha256 here and push that. The formula on main always points at the
# last tag, never at main itself.
class Kranq < Formula
  desc "Runs CI jobs in disposable Lima VMs on a macOS build machine"
  homepage "https://github.com/simon-em/kranq"
  url "https://github.com/simon-em/kranq/archive/refs/tags/v0.1.0.tar.gz"
  sha256 "a07c649ccbf111aa18a1fad068e9c265aa9c578b28906a6a9899129ab9095f00"
  # No license line: the repository carries no LICENSE file, and naming one here
  # would assert something untrue. Add a LICENSE and then say so.

  head "https://github.com/simon-em/kranq.git", branch: "main"

  depends_on "go" => :build
  depends_on :macos

  def install
    # std_go_args already passes -s -w; adding them again shows up duplicated in
    # the build line for no benefit.
    ldflags = %W[
      -X github.com/simon-em/kranq/internal/cli.Version=#{version}
    ]
    system "go", "build", *std_go_args(ldflags: ldflags)
  end

  def caveats
    <<~EOS
      brew installed the binary. Everything else kranq needs is installed by
      kranq itself, on purpose: it keeps its own Lima under ~/.kranq/deps and
      calls it by absolute path, so a Homebrew lima appearing or disappearing
      cannot change what runs.

        kranq install --deps-only               # fetch and verify lima
        kranq install --deps-only --with-daemon # and run the daemon at login
        kranq doctor                            # check this machine

      Upgrade with `brew upgrade kranq`, not `kranq upgrade`: kranq refuses to
      replace a binary a package manager owns, because the next brew upgrade
      would undo it.
    EOS
  end

  service do
    run [opt_bin/"kranq", "daemon", "run"]
    keep_alive successful_exit: false
    log_path var/"log/kranq.log"
    error_log_path var/"log/kranq.log"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/kranq version")

    (testpath/"spec.yaml").write <<~YAML
      name: brew-test
      steps:
        - name: it compiles
          run: echo hello
    YAML
    # validate and render need no daemon, no VM and no network, so they are the
    # right things for a sandboxed test to prove the binary works.
    system bin/"kranq", "validate", testpath/"spec.yaml"
    assert_match "echo hello", shell_output("#{bin}/kranq render #{testpath}/spec.yaml")
  end
end
