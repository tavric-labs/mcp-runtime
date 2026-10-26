import { fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { RowActions } from "./RowActions";

function setup() {
  const onRun = vi.fn();
  render(
    <>
      <button type="button">outside</button>
      <RowActions label="Actions for grant g1"><button type="button" onClick={onRun}>Disable</button></RowActions>
    </>,
  );
  const details = screen.getByText("Actions").closest("details") as HTMLDetailsElement;
  return { onRun, details };
}

describe("RowActions", () => {
  it("closes after an action is chosen", async () => {
    const { onRun, details } = setup();
    details.open = true;
    await userEvent.click(screen.getByRole("button", { name: "Disable" }));
    expect(onRun).toHaveBeenCalledTimes(1);
    expect(details.open).toBe(false);
  });

  it("closes on Escape and returns focus to the toggle", () => {
    const { details } = setup();
    details.open = true;
    fireEvent.keyDown(screen.getByRole("button", { name: "Disable" }), { key: "Escape" });
    expect(details.open).toBe(false);
    expect(screen.getByText("Actions").closest("summary")).toHaveFocus();
  });

  it("closes on an outside pointer press but not an inside one", () => {
    const { details } = setup();
    details.open = true;
    fireEvent.pointerDown(screen.getByRole("button", { name: "Disable" }));
    expect(details.open).toBe(true);
    fireEvent.pointerDown(screen.getByRole("button", { name: "outside" }));
    expect(details.open).toBe(false);
  });
});
