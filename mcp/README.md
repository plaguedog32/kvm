# MCP built into JetKVM firmware

An AI client connects directly to `http://<jetkvm-address>/mcp` or the device's
configured HTTPS endpoint. No local MCP server or software on the attached
computer is needed. A vision-capable client can operate firmware menus, Windows
Setup and the installed OS.

## Build and enable

Follow [DEVELOPMENT.md](../DEVELOPMENT.md) to build and deploy this checkout. Rebuild
the native library: do not reuse a stale `SKIP_NATIVE_IF_EXISTS=1` build. Go 1.25+
is required. The application update bundles native capture, web UI and MCP; it
does not require a base system image or bootloader replacement. Keep a working
application binary for rollback.

Use `./scripts/dev_deploy.sh --install -r <jetkvm-address>` for a persistent
application install; the default development mode runs a temporary debug app.

In **Settings → Access → AI agent access (MCP)** click **Enable MCP** and save the
token shown once. Add the device URL and bearer token to your remote MCP client:

```json
{
  "mcpServers": {
    "jetkvm": {
      "url": "http://192.168.1.100/mcp",
      "headers": { "Authorization": "Bearer <your-device-MCP-token>" }
    }
  }
}
```

Use your client's secret mechanism and device HTTPS where supported. This uses
manually configured bearer authentication, not OAuth discovery/login. Your client
must support HTTP authentication and have network access to JetKVM. No cloud relay
is added; existing HTTP/HTTPS bind restrictions apply.

MCP starts disabled, including with password-free browser access. Browser cookies
and login passwords do not authenticate MCP. Only the token hash is stored in
`/userdata/jetkvm/mcp-access.json`, with owner-only permissions. **Replace token**
revokes the old token; **Disable MCP** revokes access. Both cancel in-flight MCP
input. The local UI manages access through GET, POST and DELETE `/mcp/access`.
POST returns the new token once. Cross-origin browser requests are rejected.

HTTP and HTTPS share one token and input controller. Revocation takes effect in
memory before storage updates. Storage failures are reported; verify storage
before restarting if persistent revocation could not be saved. A corrupt access
file leaves MCP disabled but can be replaced through the Access page.

## Screenshots and input

MCP creates no WebRTC Session. It copies the next raw HDMI frame and encodes a JPEG
on the device while the browser's encoder/viewer stays connected. Without a
browser, capture temporarily starts and then stops the pipeline. Missing signal
returns an error; old frames are never reused. Resume a browser-held video pause
before capturing. Avoid simultaneous human and agent typing: USB has one input
state. MCP input jobs serialize; `cancel_input` stops the current job. Disable MCP
to cancel all requests associated with its token.

With **Hide display when idle** enabled, connect a browser viewer or disable that
setting before taking screenshots. MCP returns a clear error instead of toggling
HDMI on every poll. Standalone captures restart the configured idle sleep timer.

| Tool | Purpose |
| --- | --- |
| `get_status` | HDMI, USB, keyboard LEDs, media and browser session count. |
| `screenshot` | Fresh full-resolution HDMI JPEG and dimensions. |
| `press_key`, `type_text` | USB keyboard input; Meta is Windows. Text uses US layout with Caps Lock off; newlines press Enter. |
| `mouse`, `scroll` | USB pointer/wheel input; coordinates 0..1 relative to the full image. |
| `list_media` | Images uploaded through the existing UI. |
| `mount_media`, `unmount_media` | Attach/eject a URL or uploaded image as CDROM or Disk. |
| `powershell` | Submit an encoded script into an already focused target console. |
| `wait_for_screen` | Wait 0..30 seconds, then capture a fresh screenshot. |
| `cancel_input` | Cancel the running MCP input job. |

Transport uses the official MCP Go SDK's stateless Streamable HTTP handler. There
is no connect/disconnect tool or viewer slot. Requests are capped at three minutes
and 64 KiB. Increase the client's tool timeout for long typing jobs. Commands are
never retried automatically. Keys/buttons are released on completion, failure or
cancellation while USB remains available; an unavailable USB link can prevent
release. Inspect the target before repeating partial/canceled input.

`type_text` jobs estimated to take more than two minutes are rejected before input;
split the text or reduce `delay_ms`. Eject mounted virtual media before attaching
another image.

## Windows setup

Select the **`windows_setup`** prompt and provide edition/version, ISO, exact target
disk, partition policy, locale/account requirements and desired configuration.
The agent uses screenshots for boot selection, Setup, reboots, OOBE and scripts.
Enabling MCP does not install Windows or erase disks automatically.

ISO URLs must be reachable **from JetKVM**, support byte ranges and report image
size. Alternatively upload media through the normal UI and mount by filename.
Media changes can briefly rebind USB. Supply your own installation media/license.

Before `powershell`, open a target console and verify focus. For example: Meta+R,
type `powershell.exe`, Enter. The tool types `powershell.exe -NoLogo -NoProfile
-EncodedCommand ...` using UTF-16LE and presses Enter. It preserves execution
policy and does not open/elevate a console, run a shell on JetKVM, capture stdout,
wait for script completion or return an exit code. Standard WinPE may not have
PowerShell; use `type_text` for cmd commands there. Encoded commands are capped at
7500 characters. Input acknowledgements mean sent, not verified execution: inspect
screenshots after meaningful actions and before retrying canceled/timed-out input.

## Verification

Run `go test -race ./internal/mcpserver ./internal/videoimage`. The C fresh-frame
test can be built with `cc -std=c11 -Wall -Wextra -Werror -pthread
-Iinternal/native/cgo internal/native/tests/snapshot_test.c
internal/native/cgo/snapshot.c -o /tmp/jetkvm-snapshot-test` and then executed.
Also build the full ARM/native firmware. Hardware validation should check captures
with and without a browser, plus browser viewing during agent input. Automated
tests do not install Windows.

Hardware validation on 2026-11-08 covered fresh 1920x1080 captures with and
without a browser viewer, browser coexistence during agent input, HID commands
in Windows Setup, and mounting a locally stored Server 2025 ISO. Standalone
capture took roughly 4–9 seconds under media-transfer load; the native RPC allows
15 seconds for capture and JPEG encoding. The ARM application builds successfully.
The root test binary cannot start in the available QEMU environment, so root
lifecycle tests were also run on the host with test-only native stubs.
The live test also deployed Server 2025 Standard Evaluation with Desktop
Experience to the attached Ryzen AI Max+ 395 workstation through MCP keyboard
input and virtual media. Post-install checks verified hardware GPU compute and
96 GiB of dedicated video memory with Secure Boot enabled.

## Paired Windows clipboard integration

The firmware can also host a paired USB clipboard channel. It reuses the vendor
wake HID collection without adding USB interfaces or endpoints. Guest integration
is disabled by default and independent of the MCP bearer token. Enabling or
disabling reconnects USB briefly; replacing pairing revokes the previous service.

The Access page downloads a private installer ZIP containing the Windows amd64
companion, an integrity manifest, an elevated installation script, and a one-time
pairing configuration. Keep the ZIP private and delete it after installing. The
Windows LocalSystem service starts automatically, protects pairing with machine
DPAPI and SYSTEM/Administrator ACLs, and launches a helper in the active physical
console session. The helper receives no pairing key. Clipboard operations require
an unlocked default desktop and are rejected on login, lock, RDP, or secure desktops.
The companion opens no network listener and provides no shell API.

MCP tools `guest_status`, `get_clipboard`, `set_clipboard`, and `paste_text` expose
request-only Unicode clipboard access, bounded to 256 KiB of UTF-8 text. The browser
Paste dialog supports the same channel locally or through WebRTC. Reading the
remote clipboard is explicit, and clipboard contents are not logged or retained
as history. `paste_text` updates the clipboard and sends Ctrl+V to the focused
application; it does not press Enter. `powershell` supports `use_clipboard:true`
for its encoded command, while preserving the existing focused-console requirement.
Pasted line breaks can execute commands in some consoles even without an Enter
keypress. Ctrl+V uses the USB key position and requires a US-compatible target
layout. Suspended USB targets are never woken by guest requests or handshake
resets; wake the host explicitly first.
The clipboard path pastes without Enter: inspect the command before submitting
separately. The typed HID path still presses Enter. Clipboard-based submission
cannot prevent another application from replacing the Windows clipboard.
A failed transfer never automatically falls back to keyboard typing.

Windows-to-device output reports wait for per-report acknowledgements because
the kernel control-report path has one overwriting buffer. Reports are ACKed
only after framing validation, so an interrupted old response cannot keep
advancing during reconnection. See the
[JetKVM f_hid implementation](https://github.com/jetkvm/rv1106-system/blob/dev/sysdrv/source/kernel/drivers/usb/gadget/function/f_hid.c).
Clipboard text is base64 in USB/pipe JSON, fixing expansion at 4/3. Requests
have independent deadlines allowing up to 10 ms per report plus 15 seconds; failed or missing
acknowledgements close the transport. Windows history/cloud clipboard are excluded
using the [documented clipboard formats](https://learn.microsoft.com/en-us/windows/win32/dataxchg/clipboard-formats#cloud-clipboard-and-clipboard-history-formats).

USB messages use mutual challenge authentication, direction-specific AES-GCM keys
and strictly sequenced messages. Cancellation, USB rebind, and pairing changes
terminate the channel; reconnection authenticates again. The pre-installation HID
keyboard, mouse, media and screenshot tools continue to work without the companion.

`make build_guest` builds the companion into the firmware assets; development and
release firmware builds include it automatically. Tests cover malformed reports,
authentication, tampering, replay, Unicode boundaries, cancellation, generation
revocation, viewer isolation and transfer bounds. Windows DPAPI and session checks
also have native tests. Elevated ACL and physical USB/clipboard behavior still
require hardware validation.

Reinstall update: regular Windows Server 2025 Standard Desktop Experience
26100.33650 was clean-installed from the user-supplied non-evaluation ISO. The
machine is domain joined and the product key is deferred (Retail channel).
