import { useEffect, useState } from "react";
import api from "@/api";
import { DEVICE_API } from "@/ui.config";
import { Button } from "@components/Button";
import { SettingsItem } from "@components/SettingsItem";
import { SettingsSectionHeader } from "@components/SettingsSectionHeader";
import { m } from "@localizations/messages.js";
export function GuestAccessSettings() {
  const [enabled, setEnabled] = useState<boolean | null>(null);
  const [available, setAvailable] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const refresh = async () => {
    const response = await api.GET(`${DEVICE_API}/guest/access`);
    if (!response.ok) throw new Error();
    const state = (await response.json()) as {
      status: { enabled: boolean };
      installer_available: boolean;
    };
    setEnabled(state.status.enabled);
    setAvailable(state.installer_available);
  };
  useEffect(() => {
    void refresh().catch(() => setError(m.guest_settings_failed()));
  }, []);
  const update = async (disable: boolean) => {
    setBusy(true);
    setError("");
    try {
      const response = disable
        ? await api.DELETE(`${DEVICE_API}/guest/access`)
        : await api.POST(`${DEVICE_API}/guest/access`, {});
      if (!response.ok) throw new Error();
      if (!disable) {
        const blob = await response.blob();
        const url = URL.createObjectURL(blob);
        const a = document.createElement("a");
        a.href = url;
        a.download = "jetkvm-guest-private.zip";
        a.click();
        setTimeout(() => URL.revokeObjectURL(url), 60000);
      }
      await refresh();
    } catch {
      setError(m.guest_settings_failed());
      await refresh().catch(() => {});
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="space-y-4">
      <SettingsSectionHeader
        title={m.guest_settings_title()}
        description={m.guest_settings_description()}
      />
      <SettingsItem
        title={enabled ? m.guest_enabled() : m.guest_disabled()}
        description={m.guest_private_bundle_warning()}
      >
        <div className="flex gap-2">
          <Button
            size="SM"
            theme="light"
            text={enabled ? m.guest_replace() : m.guest_enable()}
            disabled={busy || enabled === null || !available}
            onClick={() => void update(false)}
          />
          {enabled && (
            <Button
              size="SM"
              theme="light"
              text={m.guest_disable()}
              disabled={busy}
              onClick={() => void update(true)}
            />
          )}
        </div>
      </SettingsItem>
      {error && (
        <p className="text-sm text-red-600" role="alert">
          {error}
        </p>
      )}
    </div>
  );
}
