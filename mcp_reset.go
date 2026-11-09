package kvm

import (
	"os"

	"github.com/jetkvm/kvm/internal/mcpserver"
)

func resetMCPAccess() {
	revokeMCPForReset(getMCPService().access, mcpAccessPath)
	if err := getGuestService().access.Disable(); err != nil {
		logger.Warn().Err(err).Msg("could not persist guest revocation during reset")
	}
	if err := os.Remove(guestAccessPath); err != nil && !os.IsNotExist(err) {
		logger.Warn().Err(err).Msg("could not remove guest pairing during reset")
	}
}

func revokeMCPForReset(access *mcpserver.Access, path string) {
	// Recovery operations must proceed even on a full or read-only filesystem.
	// Disable cancels live calls before it tries to persist the revocation.
	if err := access.Disable(); err != nil {
		logger.Warn().Err(err).Msg("could not persist MCP revocation during reset")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		logger.Warn().Err(err).Msg("could not remove MCP access settings during reset")
	}
}
