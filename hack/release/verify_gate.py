"""Read-only, fail-closed verification of a candidate's GitHub release evidence."""
import argparse
import json
import re
import subprocess


REQUIRED_JOBS = (
    'Release gate', 'Static + Generated Checks', 'Root, CLI + Integration Tests',
    'Automation Regression', 'UI unit and browser regressions',
    'Plugin Tests', 'Native registry authorization acceptance',
    'Protocol and transport matrix', 'Benchmarks', 'Repository SBOM',
    'Gosec Scan', 'Gitleaks Scan', 'Trivy Repository + Image Scan',
    'macOS companion / Compile universal companion preview',
    'Scoped observability / Cilium Scoped Observability',
) + tuple(f'Platform Service Tests ({service})' for service in (
    'platform-api', 'runtime-api', 'analytics-api', 'ingest', 'processor', 'mcp-gateway', 'ui',
)) + tuple(f'Release CLI cross-platform builds / Build {target}' for target in (
    'linux-amd64', 'linux-arm64', 'darwin-amd64', 'darwin-arm64', 'windows-amd64',
)) + tuple(f'Kind ({mode}, {replicas}) / Full QA E2E' for mode in ('org', 'public') for replicas in (1, 2)) + tuple(
    f'Staging ({mode}, {auth}) / staging-e2e' for mode in ('org', 'public') for auth in ('disabled', 'enabled')
)


def validate_run(run, commit, repository):
    if (run.get('head_sha') != commit or run.get('status') != 'completed'
            or run.get('conclusion') != 'success' or run.get('event') != 'workflow_dispatch'
            or run.get('path') != '.github/workflows/pre-release-regression.yaml'
            or (run.get('head_repository') or {}).get('full_name') != repository):
        raise ValueError('Pre-release Regression must succeed on this exact commit in this repository')


def validate_jobs(jobs):
    for name in REQUIRED_JOBS:
        matches = [job for job in jobs if job.get('name') == name]
        if len(matches) != 1 or matches[0].get('status') != 'completed' or matches[0].get('conclusion') != 'success':
            raise ValueError(f'required release check missing, incomplete or unsuccessful: {name}')
    if any(job.get('conclusion') not in ('success', 'skipped') for job in jobs):
        raise ValueError('a release check did not succeed')


def api(path):
    return json.loads(subprocess.check_output(['gh', 'api', path], text=True))


def verify(repository, commit):
    if not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+', repository) or not re.fullmatch(r'[0-9a-f]{40}', commit):
        raise ValueError('expected OWNER/REPO and a full source commit SHA')
    runs = api(f'repos/{repository}/actions/workflows/pre-release-regression.yaml/runs?head_sha={commit}&event=workflow_dispatch&per_page=100')['workflow_runs']
    if not runs:
        raise ValueError('no Pre-release Regression exists for this exact commit')
    # The latest attempt is authoritative; an older success cannot hide a rerun failure.
    run = max(runs, key=lambda item: item['id'])
    validate_run(run, commit, repository)
    jobs = []
    page = 1
    while True:
        batch = api(f'repos/{repository}/actions/runs/{run["id"]}/jobs?filter=latest&per_page=100&page={page}')['jobs']
        jobs.extend(batch)
        if len(batch) < 100:
            break
        page += 1
    validate_jobs(jobs)
    print(f'Release gate passed: {commit} (Pre-release Regression {run["id"]})')


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--repository', required=True)
    parser.add_argument('--commit', required=True)
    args = parser.parse_args()
    try:
        verify(args.repository, args.commit)
    except (ValueError, KeyError, subprocess.CalledProcessError) as error:
        parser.exit(1, f'Release blocked: {error}\n')
