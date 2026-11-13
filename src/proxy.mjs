const SUPPORTED = new Set(['socks5', 'socks5h', 'http', 'https']);

export function normalizeProxy(input) {
  if (!input) return null;
  let raw = String(input).trim();
  if (!raw) return null;
  if (raw.includes(':/x')) raw = raw.replace(':/x', '://');
  const url = new URL(raw);
  const protocol = url.protocol.replace(':', '');
  if (!SUPPORTED.has(protocol)) {
    throw new Error(`unsupported proxy protocol: ${protocol}`);
  }
  if (!url.hostname || !url.port) {
    throw new Error(`proxy must include host and port: ${raw}`);
  }
  return {
    raw,
    protocol,
    host: url.hostname,
    port: url.port,
    username: url.username || '',
    password: url.password || ''
  };
}

export function goProxyUrl(proxy) {
  if (!proxy) return null;
  // Go's http/websocket stacks are reliable with socks5://; libcurl (git) uses
  // socks5h:// for remote DNS. Keep git on the original URL.
  if (proxy.protocol === 'socks5h') {
    return `socks5://${proxy.host}:${proxy.port}`;
  }
  return proxy.raw;
}

export function goEnv(proxy) {
  const env = { NO_PROXY: '127.0.0.1,localhost,::1', no_proxy: '127.0.0.1,localhost,::1' };
  if (!proxy) return env;
  const value = goProxyUrl(proxy);
  env.HTTPS_PROXY = value;
  env.HTTP_PROXY = value;
  env.ALL_PROXY = value;
  env.https_proxy = value;
  env.http_proxy = value;
  env.all_proxy = value;
  return env;
}

export function gitProxyEntries(proxy) {
  if (!proxy) return [];
  return [
    ['http.proxy', proxy.raw],
    ['https.proxy', proxy.raw]
  ];
}

export function curlProxyArgs(proxy) {
  if (!proxy) return [];
  if (proxy.protocol === 'socks5h') {
    return ['--socks5-hostname', `${proxy.host}:${proxy.port}`];
  }
  if (proxy.protocol === 'socks5') {
    return ['--socks5', `${proxy.host}:${proxy.port}`];
  }
  return ['-x', proxy.raw];
}

export function proxyLabel(proxy) {
  return proxy ? `${proxy.protocol}://${proxy.host}:${proxy.port}` : 'direct';
}
