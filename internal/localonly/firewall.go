package localonly

import (
	"context"
	_ "embed"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

//go:embed firewall.sh
var FirewallScript string

func ApplyFirewall(ctx context.Context) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("system firewall requires Linux")
	}
	cmd := exec.CommandContext(ctx, "sh", "-s", "--", "apply")
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = strings.NewReader(strings.ReplaceAll(FirewallScript, "\r\n", "\n"))
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("private-LAN firewall unavailable: %w: %s", err, output)
	}
	return nil
}
