package mcpserver

const WindowsWorkflow = `Install and configure Windows through the JetKVM's HDMI and USB HID connection.

Collect Windows edition/version, ISO URL or uploaded filename, target physical disk, partition policy (preserve or erase), locale/account requirements and post-install configuration. Verify exact disk identity and the user's partition policy before deleting or formatting anything. Use legitimate media and the user's license. Never invent credentials.

1. Call get_status and screenshot. MCP runs directly on the device; no connect or WebRTC viewer is needed. A human can keep watching in the browser. Avoid simultaneous human and agent keyboard input. Inspect the actual screen before each sequence of actions.
2. Mount the chosen ISO with mount_media as CDROM. URL media must be reachable from the KVM itself and support byte-range requests with a known size. Alternatively upload it first using JetKVM's UI and mount by filename. Confirm media state.
3. Open this machine's firmware boot menu with press_key or restart using its visible OS interface. Select the virtual CDROM. Boot keys vary. Do not reboot the JetKVM firmware to restart the target computer.
4. Follow Windows Setup with screenshots, keys, clicks and text. Select US keyboard layout before typing. Verify edition, disk identity/capacity and partition policy before applying changes. Do not infer permission to erase a disk from a generic setup request.
5. Wait and inspect during installation. Target reboots can interrupt HDMI or change resolution. Avoid booting the ISO again after the first reboot. Do not eject media until Setup no longer needs it. Missing HDMI or snapshot timeout is not evidence of success.
6. Complete supported out-of-box setup for the requested region, account, network and privacy settings. Ask for missing requirements when needed. Verify that the desktop appears before reporting installation complete.
7. Open a target console using the desktop UI or Meta+R, powershell.exe, Enter. Verify focus and obtain elevation through the normal Windows UI if needed. powershell types a UTF-16LE encoded command into this focused console and presses Enter. It does not execute on JetKVM, return stdout or report exit status. Standard WinPE may not have PowerShell; use type_text with cmd commands there. Split long scripts into small calls. Configure requested updates/drivers/applications, then inspect output.
8. Verify requested configuration, reboot the target if required, inspect the resulting desktop and report observed results and remaining work.

Mouse coordinates are normalized 0..1 relative to the full screenshot. Text assumes US layout with Caps Lock off. Newlines press Enter and can execute commands. Input acknowledgements mean sent, not verified execution. Never blindly retry a timed-out or canceled action: it may be partially applied. Inspect the screen first. A human can stop MCP input with cancel_input or disable/revoke MCP access in Settings > Access.`
