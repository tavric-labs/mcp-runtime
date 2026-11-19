#!/usr/bin/env python3
"""Read-only public module probes; emit no response bodies or signed URLs."""
import datetime
import email.utils
import json
import os
import time
import urllib.error
import urllib.parse
import urllib.request

HOSTS = frozenset(('proxy.golang.org', 'storage.googleapis.com'))
MODULES = (
    ('github.com/segmentio/encoding', 'v0.5.4'),
    ('github.com/klauspost/compress', 'v1.20.3'),
    ('github.com/pierrec/lz4/v4', 'v4.1.33'),
    ('github.com/klauspost/compress', 'v1.20.1'),
)


def safe_host(url):
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme != 'https' or parsed.hostname not in HOSTS or parsed.username or parsed.password or parsed.port not in (None, 443):
        raise ValueError('unexpected redirect')
    return parsed.hostname


class SafeRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, msg, headers, newurl):
        safe_host(newurl)
        return super().redirect_request(request, fp, code, msg, headers, newurl)


def metadata(response):
    url = response.geturl()
    result = {'final_host': safe_host(url), 'signed_redirect_expired': None, 'server_date_utc': None}
    expires = urllib.parse.parse_qs(urllib.parse.urlsplit(url).query).get('Expires', [])
    if len(expires) == 1:
        try:
            result['signed_redirect_expired'] = float(expires[0]) < time.time()
        except ValueError:
            pass
    headers = response.headers or {}
    try:
        result['server_date_utc'] = email.utils.parsedate_to_datetime(headers.get('Date', '')).isoformat()
    except (TypeError, ValueError, AttributeError):
        pass
    return result


def probe(url, request_fn=None):
    safe_host(url)
    if request_fn is None:
        request_fn = urllib.request.build_opener(SafeRedirect()).open
    request = urllib.request.Request(url, headers={'User-Agent': 'Go-http-client/1.1'})
    result = {'http_status': None, 'error_category': None}
    try:
        with request_fn(request, timeout=20) as response:
            result.update(metadata(response))
            result['http_status'] = response.status
    except urllib.error.HTTPError as error:
        try:
            result.update(metadata(error))
            result['http_status'] = error.code
            body = error.read(4096).lower()
            result['error_category'] = 'unrecognized_http_error'
            for marker, category in ((b'request has expired', 'expired_request'), (b'expiredtoken', 'expired_token'), (b'signaturedoesnotmatch', 'signature_mismatch'), (b'requesttimetooskewed', 'clock_skew'), (b'accessdenied', 'access_denied')):
                if marker in body:
                    result['error_category'] = category
                    break
        finally:
            error.close()
    except ValueError:
        result['error_category'] = 'blocked_redirect'
    except (urllib.error.URLError, TimeoutError, OSError):
        result['error_category'] = 'tls_or_transport_error'
    return result


def main():
    checks = []
    for module, version in MODULES:
        for artifact in ('info', 'zip'):
            result = probe('https://proxy.golang.org/' + module + '/@v/' + version + '.' + artifact)
            checks.append({'module': module + '@' + version, 'artifact': artifact, **result})
    print(json.dumps({'schema_version': 1, 'utc': datetime.datetime.now(datetime.timezone.utc).isoformat(),
                      'proxy_environment_present': {key: bool(os.environ.get(key)) for key in ('HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY', 'http_proxy', 'https_proxy', 'all_proxy')},
                      'checks': checks}, indent=2, sort_keys=True))


if __name__ == '__main__':
    main()
