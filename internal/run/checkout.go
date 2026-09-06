package run

import (
	"fmt"
	"strings"
)

type Remote struct {
	Base  string
	Repo  string
	Token string
}

func (r Remote) sshURL() string {
	return fmt.Sprintf("%s/%s.git", strings.TrimSuffix(r.Base, "/"), r.Repo)
}

func (r Remote) httpsURL() string {
	host, workspace := splitSSHBase(r.Base)
	return fmt.Sprintf("https://%s/%s/%s.git", host, workspace, r.Repo)
}

func splitSSHBase(base string) (host, workspace string) {
	trimmed := strings.TrimPrefix(base, "git@")
	host, workspace, found := strings.Cut(trimmed, ":")
	if !found {
		return "bitbucket.org", strings.TrimPrefix(base, "/")
	}
	return host, workspace
}

// URL is the address the host itself uses, which is https whenever a token was
// forwarded and ssh otherwise. It has to agree with CloneCommand, or the fence
// and the checkout would authenticate as different principals.
func (r Remote) URL() string {
	if r.Token == "" {
		return r.sshURL()
	}
	return r.httpsURL()
}

func (r Remote) CloneCommand(ref, dest string) string {
	if r.Token == "" {
		return fmt.Sprintf("git clone --depth 1 --branch %s %s %s",
			shellQuote(ref), shellQuote(r.sshURL()), dest)
	}
	helper := `'!f() { echo username=x-token-auth; echo "password=$KRANQ_GIT_TOKEN"; }; f'`
	return fmt.Sprintf("git -c credential.helper= -c credential.helper=%s clone --depth 1 --branch %s %s %s",
		helper, shellQuote(ref), shellQuote(r.httpsURL()), dest)
}

func ResolveToken(env map[string]string) string {
	for _, name := range []string{"KRANQ_GIT_TOKEN", "BITBUCKET_TOKEN", "BITBUCKET_API_TOKEN", "BITBUCKET_STEP_OAUTH_TOKEN"} {
		if v := env[name]; v != "" {
			return v
		}
	}
	return ""
}

func shellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'"
}
