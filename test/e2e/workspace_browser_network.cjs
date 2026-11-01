// Public asset boundary for the password-only workspace browser acceptance.
async function externalRequestAction(request) {
  const target = new URL(request.url());
  const headers = await request.allHeaders();
  if (request.method() !== 'GET' || headers.authorization || headers.cookie || target.username || target.password) return 'block';
  // This suite exercises password sign-in against the real platform. Prevent
  // the optional Google SDK from contacting an identity provider or rendering
  // live provider frames. Never stub another path, navigation, or a POST.
  if (target.href === 'https://accounts.google.com/gsi/client' && request.resourceType() === 'script') return 'stub-google';
  if (['https://fonts.googleapis.com', 'https://fonts.gstatic.com'].includes(target.origin)) return 'continue';
  return 'block';
}

module.exports = { externalRequestAction };
