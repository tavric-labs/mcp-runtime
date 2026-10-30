"""Validate required stages and source/configuration identity in staging evidence."""
import argparse
import json
from pathlib import Path

REQUIRED_STAGING = (
    'setup', 'release-profile', 'namespace-placement', 'diagnostics', 'rollouts',
    'cluster-issuer', 'certificates', 'tls-endpoints', 'platform-login',
    'platform-api', 'audience-isolation', 'registry-route', 'registry-auth',
    'image-pulls', 'ui', 'multitenancy', 'adapter-enrollment',
    'identity-diagnostics', 'governance', 'grant-monitor', 'analytics',
    'registry-route-after-user-flows', 'workspace-browser', 'account-deactivation',
)


def validate_staging(summary, commit, mode, auth):
    if (summary.get('result') != 'passed' or summary.get('sha') != commit
            or summary.get('platform_mode') != mode or summary.get('mcp_auth') != auth):
        raise ValueError('staging evidence does not match the successful source/configuration cell')
    required = REQUIRED_STAGING + (('oidc',) if auth == 'enabled' else ())
    stages = summary.get('stages', [])
    for name in required:
        matches = [stage for stage in stages if stage.get('stage') == name]
        if len(matches) != 1 or matches[0].get('status') != 'passed':
            raise ValueError(f'required staging stage missing, skipped or unsuccessful: {name}')
    if any(stage.get('status') == 'failed' for stage in stages):
        raise ValueError('staging reported a failed stage')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('summary', type=Path)
    parser.add_argument('--commit', required=True)
    parser.add_argument('--mode', required=True, choices=('org', 'public'))
    parser.add_argument('--auth', required=True, choices=('enabled', 'disabled'))
    args = parser.parse_args()
    try:
        validate_staging(json.loads(args.summary.read_text()), args.commit, args.mode, args.auth)
    except (OSError, ValueError, KeyError) as error:
        parser.exit(1, f'Release blocked: {error}\n')
    print(f'Required staging stages passed: {args.mode}/{args.auth} at {args.commit}')
