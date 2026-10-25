# Console UI verification

Evidence for the console changes based on `492fc2fe`, including the follow-up
workspace, agent, analytics and access redesign. Browser data is explicitly
fixture-backed: it verifies presentation and request contracts, not live server
authorization. Chrome was driven through Playwright at 1440×900 and 390×900.

| Workflow | Before | After |
| --- | --- | --- |
| Admin agent creation in All teams | No creation action or form | Visible Create agent identity action, explicit owning-team selector, validation, failure/retry and details link |
| Platform overview | Raw health list with a separate health page | Component cards, Grafana, confirmed restart actions and maintenance within overview |
| Resource totals | Separate directory cards and server text | Five consistent metrics with formatted numbers and independent unavailable states |
| Audit activity | Raw timestamp and text list | Local timestamps, resource/action rows and status badges |
| Desktop/mobile bounds, dark/light | No body overflow in the fixture | No body overflow in the same fixture |
| Console/page errors in the comparison | None | None |
| Overview API reads | Directory, servers, components and audit GETs | Same four routes; Refresh overview refetches all four |

The regression suite also covers owner-only team choices, member and signed-out
UI restrictions, exact create POST payload, cancel focus restoration, restart
cancellation without mutation, audit-outage isolation and the former health URL.
Axe reports no violations (including color contrast) on the overview in both
themes at both viewports. The follow-up frontend suite passes all 283 tests
across 32 files. It ran with one worker and a 30-second per-test limit because
the loaded local machine produced timeouts with the default five-second limit.
No production dependency vulnerabilities were reported by the production audit.

All 41 browser workflows passed across the final runs, including workspace and
agent tabs, analytics disclosure and text-spacing overrides, access empty
states, role boundaries and confirmation cancellation. The first run passed 20
before the temporary local runner's five-minute global timeout; 20 more passed
on resume. One blank-page startup exceeded the five-second assertion limit and
passed in an isolated retry with a 15-second assertion limit. The final Access
and Analytics checks also passed after form-focus and trace-spacing changes.
The production build and UI service race tests passed. See
[the design review](ui-design-review.md) for research and follow-up screenshots.
These fixture checks do not establish live policy enforcement.

Live Kind browser verification is **BLOCKED**: the explicit `kind-mcp-runtime`
API at its loopback endpoint timed out. There was no candidate cluster rollout,
so these screenshots must not be described as live-cluster acceptance. Re-run
the affected browser workflows against a healthy contributor cluster before
claiming that gate. No production context or resources were used.

- [Before: desktop overview](assets/ui-overview/before.png)
- [After: desktop overview](assets/ui-overview/after.png)
- [After: mobile overview, light theme](assets/ui-overview/mobile-light.png)
- [After: mobile agent creation](assets/ui-overview/create-agent-mobile.png)
