// Canonicalize IPv6 spellings for comparisons with the server's host metadata.
globalThis.LoopForgeGitSSHHost = function normalizeSSHHost(endpoint) {
  const match = /^(\[[^\]]+\]|[^:]+):(\d+)$/.exec(String(endpoint || ''));
  if (!match) return '';
  let host = match[1].toLowerCase();
  if (host.startsWith('[')) {
    try { host = new URL(`http://${host}`).hostname; } catch { return ''; }
  }
  return `${host}:${Number(match[2])}`;
};

// Address preview only. The repository API validates and pins the SSH host.
globalThis.LoopForgeGitSSH = function parseSSHRepository(raw) {
  const address = String(raw || '').trim().replace(/^['"]|['"]$/g, '').replace(/^git clone /, '').replace(/^['"]|['"]$/g, '');
  if (!address || /[\s%?#\\]/.test(address)) return null;
  let user, host, port = '22', path, cloneUrl;
  if (address.startsWith('ssh://')) {
    const parsed = /^ssh:\/\/([^@]+)@(\[[^\]]+\]|[^:/]+)(?::(\d+))?(\/.+)$/.exec(address);
    if (!parsed) return null;
    [, user, host, , path] = parsed;
    if (parsed[3]) port = String(Number(parsed[3]));
    cloneUrl = `ssh://${user}@${host.toLowerCase()}:${port}${path}`;
  } else {
    const parsed = /^([^@]+)@(\[[^\]]+\]|[^:/]+):(.+)$/.exec(address);
    if (!parsed) return null;
    [, user, host, path] = parsed;
    cloneUrl = `${user}@${host.toLowerCase()}:${path}`;
  }
  if (!/^[a-z0-9_][a-z0-9_.-]*$/i.test(user) || !/^(?:[a-z0-9][a-z0-9.-]*|\[[a-f0-9:]+\])$/i.test(host) || host.includes('..') || Number(port) < 1 || Number(port) > 65535) return null;
  const segments = path.replace(/^\//, '').split('/');
  if (segments.some(segment => !/^[a-z0-9_.~-]+$/i.test(segment) || segment === '.' || segment === '..')) return null;
  const name = segments[segments.length - 1].replace(/\.git$/, '');
  if (!name) return null;
  const endpoint = globalThis.LoopForgeGitSSHHost(`${host}:${port}`);
  if (!endpoint) return null;
  // git@github.com: stays a GitHub repository (HTTPS + token, automatic PRs),
  // the same as on the server; only an explicit provider "ssh" uses SSH there.
  if (endpoint === 'github.com:22') return null;
  // A GitLab host is not a generic Git SSH target: it has a hosting API, so the
  // GitLab adapter must claim it. Returning null here lets the caller fall
  // through to the GitLab parser, which resolves the instance and produces the
  // gitlab: identity the server expects. The check is on the host alone so a
  // dedicated SSH port (2222) still belongs to the same instance.
  if (globalThis.LoopForgeGitLabHost?.(host.toLowerCase())) return null;
  return { provider: 'ssh', host: endpoint, project: `${host.toLowerCase()}${port === '22' ? '' : ':' + port}/${segments.join('/')}`, name, cloneUrl, webUrl: '' };
};

// LoopForgeGitLabHost reports whether a host is the user's configured GitLab
// instance. The instance origin is authoritative: a self-managed GitLab is only
// recognized for the host the user actually configured it on.
globalThis.LoopForgeGitLabHost = function gitlabHost(host) {
  const raw = String(host || '').trim().toLowerCase();
  if (!raw) return false;
  const configured = globalThis.LoopForgeGitLabBase?.();
  if (!configured) return false;
  let wanted;
  try {
    wanted = new URL(configured).host.toLowerCase();
  } catch {
    return false;
  }
  // The instance origin may carry a port (a self-managed GitLab on :8929). An
  // SSH clone address carries its own port (often 2222), so the host *name* is
  // compared and the port is ignored: both spellings reach the same instance.
  const nameOf = (value) => value.replace(/:\d+$/, '');
  return nameOf(wanted) === nameOf(raw);
};
