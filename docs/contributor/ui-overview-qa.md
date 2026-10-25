# Platform overview and agent creation verification

Evidence for the console changes based on `492fc2fe`. Browser data is explicitly
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
themes at both viewports. All 26 browser tests, 278 frontend tests, the production
build, UI service race tests, and the production dependency audit passed.
The frontend suite was run with two workers after an initial unconstrained run
hit catalog-test timeouts. No production dependency vulnerabilities were reported.

Live Kind browser verification is **BLOCKED**: the explicit `kind-mcp-runtime`
API at its loopback endpoint timed out. There was no candidate cluster rollout,
so these screenshots must not be described as live-cluster acceptance. Re-run
the affected browser workflows against a healthy contributor cluster before
claiming that gate. No production context or resources were used.

- [Before: desktop overview](assets/ui-overview/before.png)
- [After: desktop overview](assets/ui-overview/after.png)
- [After: mobile overview, light theme](assets/ui-overview/mobile-light.png)
- [After: mobile agent creation](assets/ui-overview/create-agent-mobile.png)
