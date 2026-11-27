import { render, screen } from "@testing-library/react";
import { afterEach, expect, it, vi } from "vitest";
import { OperationStatus } from "./OperationStatus";
import { AppProviders } from "../providers/AppProviders";
import type { Operation } from "../api/types";

const accepted: Operation = { id: "operation-a", kind: "team.create", resource_id: "team-a", desired_revision_id: "revision-a", state: "Pending" };
afterEach(() => vi.unstubAllGlobals());

it("keeps accepted operation identity visible while execution fails", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, json: async () => ({ operation: { ...accepted, state: "Failed", error_class: "exhausted" } }) })));
  render(<AppProviders><OperationStatus operation={accepted} /></AppProviders>);
  expect(await screen.findByText("Failed")).toBeInTheDocument();
  expect(screen.getByText("operation-a")).toBeInTheDocument();
  expect(screen.getByTestId("operation-status")).toHaveTextContent("accepted resource is retained");
});

it("rejects another operation's success instead of attributing it to this acceptance", async () => {
  vi.stubGlobal("fetch", vi.fn(async () => ({ ok: true, status: 200, json: async () => ({ operation: { ...accepted, id: "operation-b", state: "Succeeded" } }) })));
  render(<AppProviders><OperationStatus operation={accepted} /></AppProviders>);
  expect(await screen.findByText(/Status temporarily unavailable/)).toBeInTheDocument();
  expect(screen.queryByText("Succeeded")).not.toBeInTheDocument();
  expect(screen.getByText("Pending")).toBeInTheDocument();
});
