//go:build linux

package localonly

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirewallOnlyReplacesOwnedChainsAndPreflightsBothFamilies(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "apply", true: "preflight-failure"}[fail], func(t *testing.T) {
			dir := t.TempDir()
			log := filepath.Join(dir, "calls")
			stub := "#!/bin/sh\nprintf '%s %s\\n' \"$(basename \"$0\")\" \"$*\" >> \"$FIREWALL_TEST_LOG\"\ncase \"$*\" in *--noflush*) cat >> \"$FIREWALL_TEST_LOG\";; *'-S OUTPUT'*) echo '-P OUTPUT ACCEPT';; esac\n"
			for _, tool := range []string{"iptables", "ip6tables", "iptables-restore", "ip6tables-restore"} {
				body := stub
				if fail && tool == "ip6tables" {
					body = "#!/bin/sh\nexit 1\n"
				}
				if err := os.WriteFile(filepath.Join(dir, tool), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("/bin/sh", "-s", "--", "apply")
			cmd.Stdin = strings.NewReader(strings.ReplaceAll(FirewallScript, "\r\n", "\n"))
			cmd.Env = append(os.Environ(), "PATH="+dir+":/usr/bin:/bin", "FIREWALL_TEST_LOG="+log)
			output, err := cmd.CombinedOutput()
			if !fail && err != nil {
				t.Fatal(err, string(output))
			}
			if fail && err == nil {
				t.Fatal("preflight failure was ignored")
			}
			data, _ := os.ReadFile(log)
			text := string(data)
			if fail {
				if strings.Contains(text, "--noflush") || strings.Contains(text, "-I OUTPUT") {
					t.Fatal("changed rules before completing both preflights")
				}
				return
			}
			if strings.Contains(text, "-F OUTPUT") || strings.Contains(text, "-P OUTPUT") {
				t.Fatal("replaced unrelated output rules")
			}
			for _, want := range []string{"10.0.0.0/8", "fc00::/7", "-A JETKVM_LAN_ONLY -j DROP", "iptables -w 2 -I OUTPUT 1", "ip6tables -w 2 -I OUTPUT 1"} {
				if !strings.Contains(text, want) {
					t.Error("missing firewall boundary", want)
				}
			}
		})
	}
}
