"use client";

import { useEffect, useState, useTransition } from "react";
import { useRouter } from "next/navigation";

export default function RefreshButton() {
  const router = useRouter();
  const [pending, startTransition] = useTransition();
  const [live, setLive] = useState(true);

  // Poll the server component every 5s so the counters feel live.
  useEffect(() => {
    if (!live) return undefined;
    const id = setInterval(() => router.refresh(), 5000);
    return () => clearInterval(id);
  }, [live, router]);

  return (
    <div className="flex items-center gap-2">
      <button
        type="button"
        onClick={() => setLive((value) => !value)}
        className="rounded-md border border-slate-700 px-3 py-1.5 text-xs font-medium text-slate-300 transition hover:border-slate-500 hover:text-white"
        aria-pressed={live}
      >
        {live ? "● Live" : "○ Paused"}
      </button>
      <button
        type="button"
        onClick={() => startTransition(() => router.refresh())}
        disabled={pending}
        className="rounded-md bg-emerald-500 px-3 py-1.5 text-xs font-semibold text-slate-950 transition hover:bg-emerald-400 disabled:opacity-60"
      >
        {pending ? "Refreshing…" : "Refresh"}
      </button>
    </div>
  );
}
