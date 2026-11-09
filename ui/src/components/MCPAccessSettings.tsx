import { useEffect, useState } from "react";
import api from "@/api";
import { DEVICE_API } from "@/ui.config";
import { Button } from "@components/Button";
import { InputFieldWithLabel } from "@components/InputField";
import { SettingsItem } from "@components/SettingsItem";
import { SettingsSectionHeader } from "@components/SettingsSectionHeader";
import { m } from "@localizations/messages.js";

export function MCPAccessSettings() {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [busy, setBusy] = useState(false);
  const [token, setToken] = useState("");
  const [error, setError] = useState("");
  useEffect(() => {
    const abort = new AbortController();
    api
      .GET(`${DEVICE_API}/mcp/access`, { signal: abort.signal })
      .then(async response => {
        if (!response.ok) throw new Error(m.access_mcp_read_error());
        const state = (await response.json()) as { enabled: boolean };
        setEnabled(state.enabled);
      })
      .catch(() => {
        if (!abort.signal.aborted) setError(m.access_mcp_read_error());
      });
    return () => abort.abort();
  }, []);
  const update = async (disable: boolean) => {
    setBusy(true);
    setError("");
    setToken("");
    try {
      const response = disable
        ? await api.DELETE(`${DEVICE_API}/mcp/access`)
        : await api.POST(`${DEVICE_API}/mcp/access`, {});
      const state = (await response.json().catch(() => null)) as {
        enabled?: boolean;
        token?: string;
      } | null;
      if (typeof state?.enabled === "boolean") setEnabled(state.enabled);
      if (!response.ok || typeof state?.enabled !== "boolean") {
        setError(
          response.status >= 500 && state?.enabled === false
            ? m.access_mcp_persist_error()
            : m.access_mcp_update_error(),
        );
        return;
      }
      setToken(state.token ?? "");
    } catch {
      setError(m.access_mcp_update_error());
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="space-y-4">
      <SettingsSectionHeader
        title={m.access_mcp_title()}
        description={m.access_mcp_description()}
      />
      <SettingsItem
        title={enabled ? m.access_mcp_enabled() : m.access_mcp_disabled()}
        description={m.access_mcp_token_description()}
      >
        <div className="flex gap-2">
          <Button
            size="SM"
            theme="light"
            text={enabled ? m.access_mcp_replace_token() : m.access_mcp_enable()}
            disabled={busy || enabled === null}
            onClick={() => void update(false)}
          />
          {enabled && (
            <Button
              size="SM"
              theme="light"
              text={m.access_mcp_disable()}
              disabled={busy}
              onClick={() => void update(true)}
            />
          )}
        </div>
      </SettingsItem>
      <InputFieldWithLabel
        label={m.access_mcp_url()}
        readOnly
        value={`${window.location.origin}/mcp`}
      />
      {token && (
        <InputFieldWithLabel
          label={m.access_mcp_token_label()}
          description={m.access_mcp_token_warning()}
          readOnly
          value={token}
          autoComplete="off"
          spellCheck={false}
          onFocus={event => event.target.select()}
        />
      )}
      {error && (
        <p className="text-sm text-red-600" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
