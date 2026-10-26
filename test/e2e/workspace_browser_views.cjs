const assert = require('node:assert/strict');

const workspaceTeam = (page, team) => page.getByRole('region', { name: team.name + ' team', exact: true });

async function verifyWorkspaceMemberships(page, teams) {
  for (const team of teams) await workspaceTeam(page, team).getByRole('heading', { name: team.name, exact: true }).waitFor();
  const regions = page.locator('section[aria-labelledby="workspace-teams-title"]').getByRole('region');
  assert.equal(await regions.count(), teams.length, 'rendered memberships match personal API rows');
}

async function openWorkspaceScope(page) {
  const disclosure = page.locator('details').filter({ has: page.getByText('How workspace access works', { exact: true }) });
  if (await disclosure.getAttribute('open') === null) await disclosure.locator('summary').click();
  await disclosure.getByText(/Deployed by me: unavailable\./).waitFor();
  return disclosure;
}

function platformMetric(page, label) {
  return page.getByRole('group', { name: 'Platform-wide totals', exact: true }).locator('.metric-card')
    .filter({ has: page.getByText(label, { exact: true }) }).locator('.metric-value');
}

async function verifyPlatformTotals(page, counts) {
  for (const [label, value] of Object.entries(counts)) {
    assert(Number.isInteger(value) && value >= 0, 'invalid directory count');
    await platformMetric(page, label).filter({ hasText: new RegExp('^' + value.toLocaleString('en-US') + '$') }).waitFor();
  }
}

module.exports = { workspaceTeam, verifyWorkspaceMemberships, openWorkspaceScope, platformMetric, verifyPlatformTotals };
