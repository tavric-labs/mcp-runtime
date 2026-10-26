import { describe, expect, it } from "vitest";

import { ADMIN_SECTIONS, adminSection } from "./adminSections";

describe("adminSection", () => {
  it("resolves a known section id", () => {
    const section = ADMIN_SECTIONS[ADMIN_SECTIONS.length - 1];
    expect(adminSection(section.id)).toBe(section);
  });

  it("sends the retired health route to Platform overview", () => {
    expect(adminSection("platform").id).toBe(ADMIN_SECTIONS[0].id);
    expect(ADMIN_SECTIONS[0].label).toBe("Platform overview");
  });
});
