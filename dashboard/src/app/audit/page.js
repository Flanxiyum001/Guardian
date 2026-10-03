import { readAudit } from "@/lib/redis";
import AuditTable from "./audit-table";

export const dynamic = "force-dynamic";
export const revalidate = 0;

export default async function AuditPage() {
  let entries = [];
  let error = null;
  try {
    entries = await readAudit(50);
  } catch (e) {
    error = String(e?.message ?? e);
  }

  return (
    <div className="min-h-screen bg-slate-950 px-6 py-12 text-slate-100">
      <div className="mx-auto max-w-6xl">
        <header className="mb-8 border-b border-slate-800 pb-6">
          <h1 className="text-3xl font-extrabold tracking-tight text-emerald-400">
            PII Leak Auditing Log
          </h1>
          <p className="mt-1 text-sm text-slate-400">
            Every intercepted request, most recent first. The table only ever
            shows masked tokens — an authorized officer can reveal the original
            values from Redis on demand.
          </p>
        </header>

        {error ? (
          <p className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-4 py-3 text-sm text-amber-300">
            Cannot reach Redis: {error}
          </p>
        ) : (
          <AuditTable entries={entries} />
        )}
      </div>
    </div>
  );
}
