package selfinstall

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const Label = "com.effetmonstre.forge"

type Service struct {
	Binary string
	Home   string
	Path   string
}

func PlistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist"), nil
}

func (s Service) Plist() string {
	var env strings.Builder
	fmt.Fprintf(&env, "    <key>FORGE_HOME</key>\n    <string>%s</string>\n", escape(s.Home))
	if s.Path != "" {
		fmt.Fprintf(&env, "    <key>PATH</key>\n    <string>%s</string>\n", escape(s.Path))
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>daemon</string>
    <string>run</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
%s  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>ProcessType</key>
  <string>Background</string>
  <key>StandardOutPath</key>
  <string>%s/daemon.log</string>
  <key>StandardErrorPath</key>
  <string>%s/daemon.log</string>
</dict>
</plist>
`, Label, escape(s.Binary), env.String(), escape(s.Home), escape(s.Home))
}

func escape(v string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(v)
}

func (s Service) Install(out io.Writer) error {
	path, err := PlistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(s.Plist()), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "wrote %s\n", path)
	target := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", target+"/"+Label).Run()
	if err := exec.Command("launchctl", "bootstrap", target, path).Run(); err != nil {
		return fmt.Errorf("launchctl bootstrap %s: %w", target, err)
	}
	fmt.Fprintf(out, "loaded %s into %s\n", Label, target)
	return nil
}

func Uninstall(out io.Writer) error {
	path, err := PlistPath()
	if err != nil {
		return err
	}
	_ = exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d/%s", os.Getuid(), Label)).Run()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Fprintf(out, "removed %s\n", Label)
	return nil
}
