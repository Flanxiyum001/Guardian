import Link from "next/link";

const links = [
  { href: "/", label: "Overview" },
  { href: "/costs", label: "Costs & Budgets" },
  { href: "/audit", label: "Audit Log" },
];

export default function Nav() {
  return (
    <nav className="border-b border-slate-800 bg-slate-950">
      <div className="mx-auto flex max-w-5xl flex-wrap items-center gap-6 px-6 py-4">
        <Link href="/" className="text-sm font-bold text-emerald-400">
          Guardian
        </Link>
        {links.map((link) => (
          <Link
            key={link.href}
            href={link.href}
            className="text-sm text-slate-400 transition hover:text-white"
          >
            {link.label}
          </Link>
        ))}
      </div>
    </nav>
  );
}
