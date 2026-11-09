import { useEffect, useRef, useState } from "react";
import { useRTCStore, useUiStore } from "@/hooks/stores";
import { guestClipboard, guestStatus, NoClipboardChange, ClipboardBusy } from "@/utils/guest";
import notifications from "@/notifications";
import { NativeClipboardKeys } from "@/utils/nativeClipboardKeys";
import { m } from "@localizations/messages.js";

// Local editors and terminals retain their normal browser clipboard behavior.
export function isLocalClipboardTarget(target: EventTarget | null): boolean {
  const element = target instanceof Element ? target : null;
  return !!element?.closest(
    'input, textarea, select, [contenteditable]:not([contenteditable="false"]), [role="textbox"], .xterm, [data-local-clipboard]',
  );
}

export interface ClipboardKeyboard {
  releaseKeys: () => void;
  restoreModifiers: (codes: string[]) => void;
  tapMeta: (code: string) => void;
  shortcut: (action: "copy" | "paste") => Promise<void>;
}
export default function useNativeClipboard(disabled: boolean, keyboard: ClipboardKeyboard) {
  const channel = useRTCStore(state => state.rpcDataChannel);
  const [readyText, setReadyText] = useState<{ text: string; channel: RTCDataChannel } | null>(
    null,
  );
  const pendingDelivery = useRef<(() => Promise<void>) | null>(null);
  const remoteCopyPending = useRef(false);
  const [busy, setBusy] = useState(false);
  const legacyCopy = useRef(false);
  const integration = useRef(keyboard);
  integration.current = keyboard;

  useEffect(() => {
    if (disabled || !channel || channel.readyState !== "open") return;
    const listeners = new AbortController();
    let operation: AbortController | null = null;
    let available = false;
    let stopped = false;
    let expiry: ReturnType<typeof setTimeout> | undefined;
    let pasteFallback: ReturnType<typeof setTimeout> | undefined;
    let latePasteUntil = 0;
    const bookkeeping = new NativeClipboardKeys();
    const { swallowed } = bookkeeping;
    let bufferedMeta: string | null = null;
    let metaUsed = false;
    const mac = /Mac|iPhone|iPad/.test(navigator.platform);
    const focused = () => document.hasFocus() && document.visibilityState === "visible";
    const localUI = () =>
      useUiStore.getState().disableVideoFocusTrap ||
      !!document.querySelector('[role="dialog"], [aria-modal="true"]');
    const eligible = (event: Event) =>
      event.isTrusted &&
      focused() &&
      !localUI() &&
      !isLocalClipboardTarget(event.target) &&
      !isLocalClipboardTarget(document.activeElement) &&
      useRTCStore.getState().rpcDataChannel === channel;
    const reset = () => {
      operation?.abort();
      operation = null;
      setBusy(false);
      setReadyText(null);
      pendingDelivery.current = null;
      remoteCopyPending.current = false;
      clearTimeout(expiry);
      clearTimeout(pasteFallback);
      bookkeeping.clear();
      bufferedMeta = null;
    };
    const refresh = async () => {
      if (!focused()) return;
      try {
        const state = await guestStatus();
        if (!stopped) available = state.enabled && state.connected && state.native_copy === true;
      } catch {
        available = false;
      }
    };
    void refresh();
    const statusTimer = setInterval(refresh, 3000); // Cached device metadata; no guest USB traffic.
    const begin = () => {
      if (operation) return null;
      const current = new AbortController();
      operation = current;
      setReadyText(null);
      clearTimeout(expiry);
      clearTimeout(pasteFallback);
      setBusy(true);
      bookkeeping.prepareRelease();
      integration.current.releaseKeys();
      return current;
    };
    const valid = (current: AbortController) =>
      !current.signal.aborted &&
      !stopped &&
      focused() &&
      !localUI() &&
      useRTCStore.getState().rpcDataChannel === channel;
    const fail = (current: AbortController, error: unknown) => {
      if (valid(current) && !(error instanceof NoClipboardChange))
        notifications.error(
          error instanceof ClipboardBusy
            ? m.guest_clipboard_busy()
            : error instanceof Error
              ? error.message
              : "Clipboard transfer failed",
        );
    };
    const finish = (current: AbortController) => {
      if (operation === current) {
        bookkeeping.finishRelease();
        operation = null;
        setBusy(false);
      }
    };
    const copy = () => {
      const current = begin();
      if (!current) return;
      remoteCopyPending.current = true;
      let confirm = async () => {};
      const text = guestClipboard("copy", "", current.signal, {
        interactive: true,
        onExport: delivery => {
          confirm = delivery;
        },
      }).then(value => {
        if (!valid(current)) throw new Error("Copy cancelled");
        return value;
      });
      // Start the browser write inside the trusted gesture. ClipboardItem accepts
      // the delayed USB result without losing activation while the transfer runs.
      let write: Promise<void>;
      const blob = text.then(value => new Blob([value], { type: "text/plain" }));
      void blob.catch(() => {});
      try {
        write =
          window.isSecureContext &&
          navigator.clipboard?.write &&
          typeof ClipboardItem !== "undefined"
            ? navigator.clipboard.write([
                new ClipboardItem({
                  "text/plain": blob,
                }),
              ])
            : Promise.reject(new Error("Browser requires another copy gesture"));
      } catch (error) {
        write = Promise.reject(error);
      }
      void write
        .then(async () => {
          if (valid(current)) {
            await confirm().catch(() => {});
            remoteCopyPending.current = false;
          }
        })
        .catch(async () => {
          const value = await text;
          if (valid(current)) {
            pendingDelivery.current = confirm;
            setReadyText({ text: value, channel });
            expiry = setTimeout(() => setReadyText(null), 30000);
          }
        })
        .catch(error => fail(current, error))
        .finally(() => finish(current));
      // Also consume rejection when the browser rejects before reading the item.
      void text.catch(() => {});
    };
    const paste = (event: ClipboardEvent) => {
      if (!available || !eligible(event)) return;
      clearTimeout(pasteFallback);
      event.preventDefault();
      event.stopImmediatePropagation();
      if (Date.now() < latePasteUntil) {
        latePasteUntil = 0;
        return;
      }
      if (operation) {
        notifications.error(m.guest_clipboard_busy());
        return;
      }
      const text = event.clipboardData?.getData("text/plain") ?? "";
      const preferExisting = remoteCopyPending.current;
      const current = begin();
      if (!current) return;
      void guestClipboard(text ? "paste" : "paste_existing", text, current.signal, {
        interactive: true,
        preferExisting,
      })
        .then(() => {
          remoteCopyPending.current = false;
        })
        .catch(error => fail(current, error))
        .finally(() => finish(current));
    };
    const keydown = (event: KeyboardEvent) => {
      // Safari may omit C/V keyup while Cmd is held. A fresh ordinary press
      // must never inherit the previous shortcut's swallowed release.
      if (bookkeeping.down(event)) {
        event.preventDefault();
        event.stopImmediatePropagation();
        return;
      }
      if (mac && event.key === "Meta" && available && eligible(event)) {
        if (!event.repeat) {
          bufferedMeta = event.code;
          metaUsed = false;
        }
        event.preventDefault();
        event.stopImmediatePropagation();
        return;
      }
      if (
        !available ||
        !eligible(event) ||
        !(event.ctrlKey || event.metaKey) ||
        event.altKey ||
        event.shiftKey ||
        !["KeyC", "KeyV"].includes(event.code)
      ) {
        if (bufferedMeta && event.metaKey) {
          metaUsed = true;
          const meta = bufferedMeta;
          bufferedMeta = null;
          if (eligible(event)) integration.current.restoreModifiers([meta]);
        }
        if (eligible(event)) integration.current.restoreModifiers(bookkeeping.takeRestore(event));
        return;
      }
      event.stopImmediatePropagation();
      swallowed.add(event.code);
      metaUsed = true;
      if (operation) {
        event.preventDefault();
        if (!event.repeat) notifications.error(m.guest_clipboard_busy());
        return;
      }
      if (event.code === "KeyC") {
        event.preventDefault();
        if (!event.repeat) copy();
      } else {
        bookkeeping.prepareRelease();
        integration.current.releaseKeys();
        latePasteUntil = 0;
        clearTimeout(pasteFallback);
        // Some browsers do not emit paste on a non-editable focus target.
        // In that case preserve the normal guest Ctrl+V, without reading local data.
        pasteFallback = setTimeout(() => {
          if (eligible(event)) {
            latePasteUntil = Date.now() + 1000;
            const current = begin();
            if (current)
              void integration.current
                .shortcut("paste")
                .catch(error => fail(current, error))
                .finally(() => finish(current));
          }
        }, 150);
      }
      // Leave paste's browser default enabled so even HTTP pages receive its
      // ClipboardEvent. The normal HID handler must not send a second Ctrl+V.
    };
    const keyup = (event: KeyboardEvent) => {
      const consumed = bookkeeping.up(event);
      if (event.key === "Meta") {
        if (bufferedMeta) {
          event.preventDefault();
          event.stopImmediatePropagation();
          if (!metaUsed) integration.current.tapMeta(bufferedMeta);
          bufferedMeta = null;
          return;
        }
      }
      if (!consumed) return;
      event.preventDefault();
      event.stopImmediatePropagation();
    };
    const onCopy = (event: ClipboardEvent) => {
      if (legacyCopy.current) return;
      if (isLocalClipboardTarget(event.target) || isLocalClipboardTarget(document.activeElement)) {
        reset();
        return;
      }
      if (!available || !eligible(event)) return;
      event.preventDefault();
      event.stopImmediatePropagation();
      if (operation) notifications.error(m.guest_clipboard_busy());
      else copy();
    };
    const onBlur = () => {
      if (!focused()) reset();
      else void refresh();
    };
    document.addEventListener("keydown", keydown, { capture: true, signal: listeners.signal });
    document.addEventListener("keyup", keyup, { capture: true, signal: listeners.signal });
    document.addEventListener("copy", onCopy, { capture: true, signal: listeners.signal });
    document.addEventListener("paste", paste, { capture: true, signal: listeners.signal });
    window.addEventListener("blur", onBlur, { signal: listeners.signal });
    window.addEventListener("focus", onBlur, { signal: listeners.signal });
    document.addEventListener("visibilitychange", onBlur, { signal: listeners.signal });
    document.addEventListener(
      "focusin",
      event => {
        if (
          legacyCopy.current ||
          (event.target instanceof Element && event.target.closest("[data-guest-copy-ready]"))
        )
          return;
        if (isLocalClipboardTarget(event.target)) {
          reset();
          integration.current.releaseKeys();
        }
      },
      { signal: listeners.signal },
    );
    const dialogs = new MutationObserver(() => {
      if (localUI()) reset();
    });
    dialogs.observe(document.body, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeFilter: ["role", "aria-modal"],
    });
    return () => {
      stopped = true;
      listeners.abort();
      clearInterval(statusTimer);
      dialogs.disconnect();
      reset();
    };
  }, [channel, disabled]);

  const copyReady = () => {
    if (
      readyText === null ||
      !document.hasFocus() ||
      document.visibilityState !== "visible" ||
      useRTCStore.getState().rpcDataChannel !== readyText.channel ||
      readyText.channel.readyState !== "open"
    )
      return;
    const field = document.createElement("textarea");
    field.value = readyText.text;
    field.setAttribute("data-local-clipboard", "");
    field.style.cssText = "position:fixed;opacity:0;pointer-events:none";
    const previous = document.activeElement as HTMLElement | null;
    (previous?.parentElement ?? document.body).append(field);
    legacyCopy.current = true;
    try {
      field.focus();
      field.select();
      if (!document.execCommand("copy")) throw new Error("Browser blocked clipboard write");
      const confirm = pendingDelivery.current;
      pendingDelivery.current = null;
      remoteCopyPending.current = false;
      if (confirm) void confirm().catch(() => {});
      setReadyText(null);
    } catch (error) {
      notifications.error(error instanceof Error ? error.message : "Copy failed");
    } finally {
      legacyCopy.current = false;
      field.remove();
      previous?.focus();
    }
  };
  return { busy, copyReady, copyPending: readyText !== null };
}
