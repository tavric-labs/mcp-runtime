# Console UX design review

Reviewed 2026-10-25 for the workspace, agent, platform, access and analytics changes in
[PR #859](https://github.com/tavric-labs/mcp-runtime/pull/859).

## Research and application

- [IBM Carbon dashboard guidance](https://www.carbondesignsystem.com/building-blocks/data-visualization/dashboards)
  prioritizes important data, limits competing metrics, uses consistent data
  colors and treats whitespace as structure. Analytics now leads with four
  request/decision/latency metrics and the traffic trend, followed by supporting
  reliability figures and request investigation.
- [NN/g progressive disclosure](https://www.nngroup.com/articles/progressive-disclosure/)
  separates common tasks from secondary options while keeping the disclosure
  easy to find. Advanced analytics filters retain their active values when
  collapsed; agent connection, activity and certificates have named views.
- [GOV.UK spacing](https://design-system.service.gov.uk/styles/spacing/) and
  [layout guidance](https://design-system.service.gov.uk/styles/layout/)
  use consistent responsive spacing and constrain prose width. This console
  retains its own tokens, with 24px field gaps, 40px section separation,
  1.6 body line height and bounded supporting prose.
- [W3C target-size guidance](https://www.w3.org/WAI/WCAG22/Understanding/target-size-minimum)
  addresses target size and clearance. Primary controls use a comfortable 44px
  height; this is a product choice, not a claim that WCAG AA requires 44px.
  [W3C text-spacing guidance](https://www.w3.org/WAI/WCAG22/Understanding/text-spacing.html)
  requires content to tolerate user overrides. Browser checks increase line,
  paragraph, letter and word spacing without body overflow.
- The implementation followed the
  [frontend-design skill](https://github.com/anthropics/skills/tree/main/skills/frontend-design)
  and [web-design-guidelines skill](https://github.com/vercel-labs/agent-skills/tree/main/skills/web-design-guidelines).
  The visual direction retains the console's Space Grotesk type and semantic
  slate/blue/status palette. Roster and history boundaries determine grouping;
  decorative gradients and repeated oversized cards are not the organizing device.

## Review checks

My workspace no longer repeats team membership and agent sections. An agent's
full ID is selectable and copyable; its primary connection action is visible.
Keyboard tab navigation retains the selected view in the URL. Read-only members
receive no management action, and namespace and agent scope checks remain intact.

Analytics uses the returned totals and time buckets. Zero requests produce no
calculated deny rate or fabricated trend. An analytics outage leaves gateway
audit available. Allowed policy decisions do not imply tool success.

Access separates labeled subject identities, status and management actions.
Empty directories guide creation; populated rows expose management controls
through an explicit Actions disclosure followed by confirmation. Summary labels
include expired grants as inactive and leave failed totals unknown. Search
counts describe the filtered rows; headline counts describe the namespace scope.

Browser fixtures exercise populated and empty states, permission boundaries,
responsive layout, light/dark contrast, advanced filters and keyboard controls.
A separate local preview checks eight surfaces at 390px, 768px and 1280px.
These are UI and request-contract checks, not live-cluster authorization evidence.
Live Kind acceptance remains blocked by the unresponsive local cluster.

The loopback preview uses an explicitly labeled sample-data backend stored only
in a temporary directory. It preserves locally created sample identities and
contains no production connection or credentials. Sample traffic is not shipped
with the application. The preview label appears in a footer outside the interactive content so it
cannot cover navigation or focused controls.

## Fixture screenshots

- [Access control, desktop dark theme](assets/ui-overview/access-1440-dark.png)
- [Access control, mobile light theme](assets/ui-overview/access-390-light.png)
- [Usage analytics, desktop dark theme](assets/ui-overview/analytics-1440-dark.png)
- [My workspace, desktop dark theme](assets/ui-overview/workspace-1440-dark.png)
- [Agent connection steps, mobile light theme](assets/ui-overview/agent-connect-390-light.png)
