const docs = [
  {
    title: "Product",
    path: "design/00_product.md",
    description: "System goals, native slice model, and workflow direction."
  },
  {
    title: "Architecture",
    path: "design/01_gitslice_architecture_design.md",
    description: "Service boundaries, storage model, and Git compatibility."
  },
  {
    title: "Core API",
    path: "design/03_core_api.md",
    description: "Current public RPC contract for browser and CLI callers."
  },
  {
    title: "Web UI",
    path: "design/11_web_interface_design.md",
    description: "Web support boundary and page-level behavior."
  }
];

export function DocPage() {
  return (
    <section className="mx-auto w-full max-w-7xl">
      <div className="border-b border-slate-200 pb-5">
        <p className="text-xs font-semibold uppercase tracking-normal text-slate-500">
          Gitslice Web
        </p>
        <h1 className="mt-2 text-2xl font-semibold tracking-normal text-zinc-950">
          Documentation
        </h1>
        <p className="mt-2 max-w-2xl text-sm leading-6 text-slate-600">
          Project design references for the current prototype. These files live
          in the repository and are the source of truth for product behavior.
        </p>
      </div>

      <div className="mt-8 grid gap-4 md:grid-cols-2">
        {docs.map((doc) => (
          <article
            className="rounded-lg border border-slate-200 bg-white p-5 shadow-sm shadow-slate-200/50"
            key={doc.path}
          >
            <h2 className="text-base font-semibold text-zinc-950">
              {doc.title}
            </h2>
            <p className="mt-2 text-sm leading-6 text-slate-600">
              {doc.description}
            </p>
            <code className="mt-4 block break-all rounded-md bg-slate-50 px-3 py-2 font-mono text-xs text-slate-700">
              {doc.path}
            </code>
          </article>
        ))}
      </div>
    </section>
  );
}
