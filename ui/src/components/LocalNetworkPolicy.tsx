import { useEffect, useState } from "react";
import { useJsonRpc } from "@/hooks/useJsonRpc";
import { m } from "@localizations/messages.js";

export function LocalNetworkPolicy() {
  const [systemFilter, setSystemFilter] = useState(false);
  const { send } = useJsonRpc();
  useEffect(() => {
    void send("getNetworkPolicyState", {}, response => {
      if ("error" in response) return;
      setSystemFilter(
        (response.result as { system_firewall_applied?: boolean }).system_firewall_applied === true,
      );
    });
  }, [send]);
  return (
    <div className="rounded bg-blue-50 p-3 text-sm dark:bg-slate-800">
      <p>{m.local_only_description()}</p>
      <p className="mt-2">
        {systemFilter
          ? m.local_only_system_filter_active()
          : m.local_only_system_filter_unverified()}
      </p>
    </div>
  );
}
