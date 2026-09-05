package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"crypto/sha256"
	"encoding/hex"

	"github.com/effetmonstre/forge/internal/daemon"
	"github.com/effetmonstre/forge/internal/deps"
	"github.com/effetmonstre/forge/internal/exitcode"
	"github.com/effetmonstre/forge/internal/ipc"
	"github.com/effetmonstre/forge/internal/selfinstall"
)

func daemonConfig() daemon.Config {
	home := forgeHome()
	return daemon.Config{
		Home:           home,
		Version:        Version,
		MaxVMs:         envInt("FORGE_MAX_VMS", 2),
		MemoryHeadroom: int64(envInt("FORGE_MEMORY_HEADROOM_MB", 2048)) << 20,
		ClaudeToken:    claudeToken(home),
		GitRemote:      envOr("FORGE_GIT_REMOTE"),
		LimaHome:       os.Getenv("FORGE_LIMA_HOME"),
	}
}

func claudeToken(home string) string {
	if v := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"); v != "" {
		return v
	}
	stored, err := daemon.LoadEnv(home)
	if err != nil {
		return ""
	}
	return stored["CLAUDE_CODE_OAUTH_TOKEN"]
}

func envInt(name string, fallback int) int {
	if v := os.Getenv(name); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func newDaemon() (*daemon.Daemon, error) {
	cfg := daemonConfig()
	if cfg.GitRemote == "" {
		cfg.GitRemote = defaultRemote
	}
	self, err := os.Executable()
	if err != nil {
		return nil, err
	}
	// The daemon does not run jobs itself. It starts `forge exec` in its own
	// process group and reads what that records, so a job outlives a restart
	// and a restarted daemon can pick it back up.
	sup := &daemon.Supervisor{Binary: self, Home: cfg.Home}
	d, err := daemon.New(cfg, sup)
	if err != nil {
		return nil, err
	}
	sup.TaskDir = d.TaskDir
	sup.RecordPGID = d.RecordPGID
	sup.RecordResult = d.RecordResult
	return d, nil
}

func runDaemon(env Env, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(env.Stderr, "usage: forge daemon run|start|stop|status")
		return exitcode.Usage
	}
	subs := map[string]func(Env, []string) int{
		"run":    daemonRun,
		"start":  daemonStart,
		"stop":   daemonStop,
		"status": daemonStatus,
	}
	sub, ok := subs[args[0]]
	if !ok {
		fmt.Fprintf(env.Stderr, "forge daemon: unknown subcommand %q\n", args[0])
		return exitcode.Usage
	}
	return sub(env, args[1:])
}

func daemonRun(env Env, args []string) int {
	d, err := newDaemon()
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Fprintf(env.Stderr, "forge daemon %s listening on %s\n", Version, daemonConfig().SocketPath())
	if err := d.Run(ctx); err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	return exitcode.OK
}

func daemonStart(env Env, args []string) int {
	client := ipc.NewClient(daemonConfig().SocketPath())
	if client.Ping(context.Background()) == nil {
		fmt.Fprintln(env.Stderr, "the daemon is already running")
		return exitcode.OK
	}
	if err := spawnDaemon(); err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	if err := client.WaitReady(context.Background(), 10*time.Second); err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.Unreachable
	}
	fmt.Fprintln(env.Stderr, "daemon started")
	return exitcode.OK
}

func spawnDaemon() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	return spawnDaemonFrom(self)
}

// The binary is explicit because an upgrade restarts the daemon from the copy
// it just installed, which is not necessarily the copy running the command.
func spawnDaemonFrom(bin string) error {
	logPath := forgeHome() + "/daemon.log"
	if err := selfinstall.EnsureHome(forgeHome()); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	cmd := exec.Command(bin, "daemon", "run")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd.Start()
}

func daemonStop(env Env, args []string) int {
	fs := flag.NewFlagSet("daemon stop", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	force := fs.Bool("force", false, "stop even while work is in flight")
	if _, err := parsePermuted(fs, args); err != nil {
		return exitcode.Usage
	}
	cfg := daemonConfig()
	client := ipc.NewClient(cfg.SocketPath())
	status, err := client.Status(context.Background())
	if err != nil {
		fmt.Fprintln(env.Stderr, "the daemon is not running")
		return exitcode.OK
	}
	if n := daemon.InFlight(status); n > 0 && !*force {
		fmt.Fprintf(env.Stderr, "forge: %d task(s) in flight (%s)\n", n, daemon.Describe(status))
		fmt.Fprintln(env.Stderr, "cancel them, wait, or pass --force to stop anyway")
		return exitcode.Misconfigured
	}
	pid, err := readPID(cfg.LockPath())
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.InternalError
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		fmt.Fprintf(env.Stderr, "forge: could not signal pid %d: %v\n", pid, err)
		return exitcode.InternalError
	}
	for range 200 {
		if client.Ping(context.Background()) != nil {
			fmt.Fprintln(env.Stderr, "daemon stopped")
			return exitcode.OK
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Fprintln(env.Stderr, "forge: the daemon is still draining in-flight work")
	return exitcode.OK
}

func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("no pid file at %s", path)
	}
	pid, err := strconv.Atoi(string(data))
	if err != nil {
		return 0, fmt.Errorf("unreadable pid file %s", path)
	}
	return pid, nil
}

func daemonStatus(env Env, args []string) int { return runStatus(env, args) }

func runStatus(env Env, args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	asJSON := fs.Bool("json", false, "emit the status as json")
	if _, err := parsePermuted(fs, args); err != nil {
		return exitcode.Usage
	}
	client, code := connect(env, false)
	if client == nil {
		return code
	}
	status, err := client.Status(context.Background())
	if err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v\n", err)
		return exitcode.Unreachable
	}
	if *asJSON {
		enc := json.NewEncoder(env.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(status)
		return exitcode.OK
	}
	fmt.Fprintf(env.Stdout, "forge %s, pid %d, up %s\n", status.Version, status.PID, status.Uptime)
	fmt.Fprintf(env.Stdout, "queue     queued=%d blocked=%d running=%d/%d lost=%d\n",
		status.Queued, status.Blocked, status.Running, status.MaxVMs, status.Lost)
	if status.StopReason != "" {
		fmt.Fprintf(env.Stdout, "waiting   %s\n", status.StopReason)
	}
	if status.Resources.OK {
		fmt.Fprintf(env.Stdout, "machine   %.1fGiB free of %.1fGiB, %d cpus\n",
			gib(status.Resources.AvailableBytes), gib(status.Resources.TotalBytes), status.Resources.CPUs)
	}
	claude := "no token"
	if status.Claude.Present {
		claude = "open"
		if !status.Claude.Available {
			claude = fmt.Sprintf("closed, next check in %s", status.Claude.Remaining.Truncate(time.Second))
			if status.Claude.LastReason != "" {
				claude += " (" + status.Claude.LastReason + ")"
			}
		}
	}
	fmt.Fprintf(env.Stdout, "claude    %s\n", claude)
	return exitcode.OK
}

func gib(n int64) float64 { return float64(n) / (1 << 30) }

func connect(env Env, autostart bool) (*ipc.Client, int) {
	cfg := daemonConfig()
	client := ipc.NewClient(cfg.SocketPath())
	if client.Ping(context.Background()) == nil {
		return client, exitcode.OK
	}
	if !autostart || os.Getenv("FORGE_AUTOSTART") == "0" {
		fmt.Fprintf(env.Stderr, "forge: no daemon at %s; start one with `forge daemon start`\n", cfg.SocketPath())
		return nil, exitcode.Unreachable
	}
	if err := spawnDaemon(); err != nil {
		fmt.Fprintf(env.Stderr, "forge: could not start the daemon: %v\n", err)
		return nil, exitcode.InternalError
	}
	if err := client.WaitReady(context.Background(), 10*time.Second); err != nil {
		fmt.Fprintf(env.Stderr, "forge: %v; see %s/daemon.log\n", err, cfg.Home)
		return nil, exitcode.Unreachable
	}
	fmt.Fprintln(env.Stderr, "forge: started the local daemon")
	return client, exitcode.OK
}

func fingerprint(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:6])
}

func limactlPath(home string) string {
	lima := deps.Lima{Root: home}
	if lima.Installed() {
		return lima.Binary()
	}
	return ""
}

// nodeName only ever appears in a fence record, so a machine that cannot name
// itself is a cosmetic problem rather than a fatal one.
func nodeName() string {
	if v := os.Getenv("FORGE_NODE"); v != "" {
		return v
	}
	host, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return host
}
