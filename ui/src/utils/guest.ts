import { useRTCStore } from "@/hooks/stores";
import { callJsonRpc } from "@/utils/jsonrpc";
export interface GuestState {
  native_copy?: boolean;
  enabled: boolean;
  connected: boolean;
  unlocked: boolean;
  user?: string;
}
interface Transfer {
  busy?: boolean;
  unchanged?: boolean;
  id: string;
  done: boolean;
  failed: boolean;
  data?: string;
  length: number;
  has_text: boolean;
}
export class NoClipboardChange extends Error {}
export class ClipboardBusy extends Error {}
export interface ClipboardOptions {
  interactive?: boolean;
  preferExisting?: boolean;
  onExport?: (confirm: () => Promise<void>) => void;
}
async function request(args: object, channel: RTCDataChannel): Promise<Transfer> {
  const response = await callJsonRpc<Transfer>({
    method: "guestClipboard",
    channel,
    params: { args },
    maxAttempts: 1,
    attemptTimeoutMs: 10000,
  });
  if (response.error)
    throw new Error("Guest clipboard request failed; clipboard or input may have changed.");
  return response.result;
}
export async function guestStatus(): Promise<GuestState> {
  const channel = useRTCStore.getState().rpcDataChannel;
  if (!channel || channel.readyState !== "open") throw new Error("Viewer is disconnected");
  const response = await callJsonRpc<GuestState>({
    method: "guestStatus",
    channel,
    maxAttempts: 1,
  });
  if (response.error) throw new Error("Guest integration unavailable");
  return response.result;
}
export async function guestClipboard(
  action: "get" | "copy" | "set" | "paste" | "paste_existing",
  text = "",
  signal?: AbortSignal,
  options: ClipboardOptions = {},
): Promise<string> {
  if (signal?.aborted) throw new Error("Cancelled");
  const bytes = new TextEncoder().encode(text);
  if (bytes.length > 262144 || text.includes("\0"))
    throw new Error("Clipboard text exceeds 256 KiB or contains NUL");
  // Reject lone UTF-16 surrogates rather than silently replacing user text.
  if (new TextDecoder().decode(bytes) !== text)
    throw new Error("Clipboard text contains invalid Unicode");
  const channel = useRTCStore.getState().rpcDataChannel;
  if (!channel || channel.readyState !== "open") throw new Error("Viewer is disconnected");
  const transferRequest = (args: object) => request(args, channel);
  const start = await transferRequest({
    action: `start_${action}`,
    interactive: options.interactive === true,
    prefer_existing: options.preferExisting === true,
  });
  try {
    if (action !== "get" && action !== "copy" && action !== "paste_existing") {
      let binary = "";
      for (const byte of bytes) binary += String.fromCharCode(byte);
      const data = btoa(binary);
      for (let offset = 0; offset < data.length; offset += 16384) {
        if (signal?.aborted) throw new Error("Cancelled");
        await transferRequest({
          action: "upload",
          id: start.id,
          offset,
          data: data.slice(offset, offset + 16384),
        });
      }
      if (signal?.aborted) throw new Error("Cancelled");
      await transferRequest({ action: "commit", id: start.id });
    }
    const deadline = Date.now() + 150000;
    let state: Transfer;
    do {
      if (signal?.aborted) throw new Error("Cancelled");
      if (Date.now() > deadline)
        throw new Error("Clipboard transfer timed out; clipboard or input may have changed");
      await new Promise(resolve => setTimeout(resolve, 100));
      state = await transferRequest({ action: "poll", id: start.id });
    } while (!state.done);
    if (state.busy) throw new ClipboardBusy();
    if (state.unchanged) throw new NoClipboardChange();
    if (state.failed && action === "copy")
      throw new Error(
        "Remote copy failed. Select text and make sure the physical console is unlocked.",
      );
    if (state.failed)
      throw new Error(
        "Guest clipboard operation failed; unlock the physical console and check the companion. Clipboard or input may have changed.",
      );
    if (action !== "get" && action !== "copy") return "";
    if (action === "copy" && !state.has_text) throw new NoClipboardChange();
    let encoded = "";
    for (let offset = 0; offset < state.length; offset += 16384) {
      if (signal?.aborted) throw new Error("Cancelled");
      const chunk = await transferRequest({
        action: "read",
        id: start.id,
        offset,
      });
      encoded += chunk.data ?? "";
    }
    const result = new TextDecoder("utf-8", { fatal: true }).decode(
      Uint8Array.from(atob(encoded), c => c.charCodeAt(0)),
    );
    if (signal?.aborted) throw new Error("Cancelled");
    if (state.has_text)
      options.onExport?.(async () => {
        await transferRequest({ action: "confirm", id: start.id });
      });
    return result;
  } finally {
    await transferRequest({ action: "cancel", id: start.id }).catch(() => {});
  }
}
