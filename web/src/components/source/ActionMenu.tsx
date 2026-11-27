import { PortalMenu } from "../ui/PortalMenu";

export interface ActionMenuItem {
  label: string;
  onSelect(): void;
  tone?: "default" | "danger";
  disabled?: boolean;
  title?: string;
  // A toggle: shown with a check when on, and announced as a checkbox.
  checked?: boolean;
  // Only on phones, for an action a wider screen has a button for.
  phoneOnly?: boolean;
}

export function ActionMenu({
  items,
  label = "Actions",
  align = "right"
}: {
  items: ActionMenuItem[];
  label?: string;
  align?: "left" | "right";
}): JSX.Element {
  const hasToggle = items.some((item) => item.checked !== undefined);

  return (
    <PortalMenu
      align={align}
      panelClassName="max-h-[60vh] min-w-36 overflow-auto p-1"
      renderTrigger={({ open, ref, toggle }) => (
        <button
          aria-expanded={open}
          aria-haspopup="menu"
          aria-label={label}
          className="inline-flex h-8 w-8 items-center justify-center rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 text-lg font-semibold leading-none text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950 active:scale-[0.98]"
          onClick={toggle}
          ref={ref}
          type="button"
        >
          <span aria-hidden="true">⋮</span>
        </button>
      )}
    >
      {({ close }) =>
        items.map((item) => {
          const isDanger = item.tone === "danger";
          // With a toggle in the list, the others line up with its label.
          const indent = hasToggle && item.checked === undefined;
          return (
            <button
              aria-checked={item.checked === undefined ? undefined : item.checked}
              className={[
                "w-full whitespace-nowrap rounded px-3 py-2 text-left text-sm font-medium transition disabled:cursor-not-allowed disabled:opacity-50",
                item.phoneOnly ? "block sm:hidden" : "block",
                indent ? "pl-8" : "",
                isDanger
                  ? "text-rose-700 dark:text-rose-300 hover:bg-rose-50 dark:hover:bg-rose-950/30"
                  : "text-slate-700 dark:text-zinc-300 hover:bg-slate-50 dark:hover:bg-zinc-950"
              ].join(" ")}
              disabled={item.disabled}
              key={item.label}
              onClick={() => {
                item.onSelect();
                close();
              }}
              role={item.checked === undefined ? "menuitem" : "menuitemcheckbox"}
              title={item.title}
              type="button"
            >
              {item.checked === undefined ? (
                item.label
              ) : (
                <span className="flex items-center gap-2">
                  <span aria-hidden="true" className="w-3 text-center">
                    {item.checked ? "✓" : ""}
                  </span>
                  {item.label}
                </span>
              )}
            </button>
          );
        })
      }
    </PortalMenu>
  );
}
