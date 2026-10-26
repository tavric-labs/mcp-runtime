import { useEffect, useRef, type ReactNode } from "react";

import { Icon } from "./Icon";

type RowActionsProps = {
  label: string;
  children: ReactNode;
};

// Disclosure menu for per-row actions. It closes after an action is chosen,
// on Escape (returning focus to the toggle) and on a click outside, so a row
// never keeps a stale open menu while its confirmation dialog is showing.
export function RowActions({ label, children }: RowActionsProps) {
  const ref = useRef<HTMLDetailsElement>(null);

  useEffect(() => {
    function onPointerDown(event: PointerEvent) {
      const details = ref.current;
      if (details?.open && event.target instanceof Node && !details.contains(event.target)) details.open = false;
    }
    document.addEventListener("pointerdown", onPointerDown);
    return () => document.removeEventListener("pointerdown", onPointerDown);
  }, []);

  return (
    <details
      ref={ref}
      className="access-row-actions"
      onKeyDown={(event) => {
        if (event.key !== "Escape" || !ref.current?.open) return;
        event.stopPropagation();
        ref.current.open = false;
        ref.current.querySelector("summary")?.focus();
      }}
    >
      <summary aria-label={label}>Actions <Icon name="chevronDown" size={14} /></summary>
      <div
        onClick={(event) => {
          if (event.target instanceof Element && event.target.closest("button") && ref.current) ref.current.open = false;
        }}
      >
        {children}
      </div>
    </details>
  );
}
