"use client";

import { useEffect, useState } from "react";

const statusStyles = {
  masked: "bg-red-500/15 text-red-400",
  clean: "bg-slate-700/60 text-slate-300",
  blocked_budget: "bg-amber-500/15 text-amber-400",
  blocked_presidio: "bg-orange-500/15 text-orange-400",
};

export default function AuditTable({ entries }) {
  const [officerKey, setOfficerKey] = useState("");
  const [revealed, setRevealed] = useState({});
  const [busyId, setBusyId] = useState(null);
  const [error, setError] = useState("");

  useEffect(() => {
    const saved = window.localStorage.getItem("guardian.auditKey");
    if (saved) setOfficerKey(saved);
  }, []);

  async function reveal(entry) {
    setError("");
    setBusyId(entry.id);
    try {
      const res = await fetch("/api/reveal", {
        method: "POST",
        headers: { "Content-Type": "application/json", "x-audit-key": officerKey },
        body: JSON.stringify({ tokens: entry.tokens || [] }),
      });
      const data = await res.json();
      if (!res.ok) {
        setError(data.message || data.error || `Request failed (${res.status})`);
        return;
      }
      window.localStorage.setItem("guardian.auditKey", officerKey);
      setRevealed((prev) => ({ ...prev, [entry.id]: data.values }));
    } catch (e) {
      setError(String(e?.message ?? e));
    } finally {
      setBusyId(null);
    }
  }

  if (entries.length === 0) {
    return (
      <p className="text-sm text-slate-500">
        No audit entries yet. Send a request through the gateway and refresh.
      </p>
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3 rounded-lg border border-slate-800 bg-slate-900 px-4 py-3">
        <label htmlFor="audit-key" className="text-xs font-semibold uppercase tracking-wider text-slate-400">
          Officer key
        </label>
        <input
          id="audit-key"
          type="password"
          value={officerKey}
          onChange={(e) => setOfficerKey(e.target.value)}
          placeholder="AUDIT_REVEAL_KEY"
          className="w-64 rounded-md border border-slate-700 bg-slate-950 px-3 py-1.5 text-sm text-slate-100 outline-none focus:border-emerald-500"
        />
        <span className="text-xs text-slate-600">
          Required to reveal original values. Stored only in your browser.
        </span>
      </div>

      {error ? (
        <p className="rounded-lg border border-red-500/40 bg-red-500/10 px-4 py-2 text-sm text-red-300">
          {error}
        </p>
      ) : null}

      <div className="overflow-x-auto rounded-xl border border-slate-800">
        <table className="min-w-full divide-y divide-slate-800 text-sm">
          <thead className="bg-slate-900 text-xs uppercase tracking-wider text-slate-500">
            <tr>
              <th className="px-4 py-3 text-left">Time</th>
              <th className="px-4 py-3 text-left">User</th>
              <th className="px-4 py-3 text-left">IP</th>
              <th className="px-4 py-3 text-left">Team / Role</th>
              <th className="px-4 py-3 text-left">Service</th>
              <th className="px-4 py-3 text-left">Status</th>
              <th className="px-4 py-3 text-left">Entities masked</th>
              <th className="px-4 py-3 text-left">Preview</th>
              <th className="px-4 py-3 text-left">Reveal</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-800 bg-slate-950">
            {entries.map((entry) => {
              const values = revealed[entry.id];
              return (
                <tr key={entry.id} className="align-top">
                  <td className="whitespace-nowrap px-4 py-3 text-slate-400">
                    {new Date(entry.ts * 1000).toLocaleString()}
                  </td>
                  <td className="px-4 py-3 text-slate-200">{entry.user}</td>
                  <td className="px-4 py-3 text-slate-500">{entry.ip}</td>
                  <td className="px-4 py-3 text-slate-400">
                    {[entry.team, entry.role].filter(Boolean).join(" / ") || "—"}
                  </td>
                  <td className="px-4 py-3 text-slate-400">{entry.service}</td>
                  <td className="px-4 py-3">
                    <span
                      className={`rounded px-2 py-0.5 text-xs font-semibold ${statusStyles[entry.status] || "bg-slate-700/60 text-slate-300"}`}
                    >
                      {entry.status}
                    </span>
                  </td>
                  <td className="px-4 py-3">
                    {entry.entities?.length ? (
                      <div className="flex flex-wrap gap-1">
                        {entry.entities.map((entity, i) => (
                          <span
                            key={`${entity}-${i}`}
                            className="rounded bg-slate-800 px-2 py-0.5 text-xs text-red-300"
                          >
                            {entity}
                          </span>
                        ))}
                      </div>
                    ) : (
                      <span className="text-slate-600">—</span>
                    )}
                  </td>
                  <td className="max-w-xs px-4 py-3">
                    <code className="break-all text-xs text-slate-400">
                      {entry.preview || "—"}
                    </code>
                    {values ? (
                      <div className="mt-2 space-y-1">
                        {(entry.tokens || []).map((token) => (
                          <div key={token} className="text-xs">
                            <span className="text-slate-500">{token}</span>
                            <span className="mx-1 text-slate-600">→</span>
                            <span className="text-emerald-300">
                              {values[token] ?? "(expired)"}
                            </span>
                          </div>
                        ))}
                      </div>
                    ) : null}
                  </td>
                  <td className="px-4 py-3">
                    {entry.tokens?.length ? (
                      values ? (
                        <button
                          type="button"
                          onClick={() =>
                            setRevealed((prev) => {
                              const next = { ...prev };
                              delete next[entry.id];
                              return next;
                            })
                          }
                          className="rounded-md border border-slate-700 px-2 py-1 text-xs text-slate-300 hover:border-slate-500"
                        >
                          Hide
                        </button>
                      ) : (
                        <button
                          type="button"
                          disabled={busyId === entry.id}
                          onClick={() => reveal(entry)}
                          className="rounded-md border border-emerald-600 px-2 py-1 text-xs text-emerald-300 hover:bg-emerald-500/10 disabled:opacity-50"
                        >
                          {busyId === entry.id ? "…" : "Reveal"}
                        </button>
                      )
                    ) : (
                      <span className="text-slate-600">—</span>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}
