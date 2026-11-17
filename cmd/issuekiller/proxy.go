package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Proxy struct {
	Raw    string
	Scheme string
	Host   string
	Port   string
	User   string
	Pass   string
}

func parseProxy(raw string) (*Proxy, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	raw = strings.Replace(raw, ":/x", "://", 1)
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy %q: %w", raw, err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "socks5", "socks5h", "http", "https":
	default:
		return nil, fmt.Errorf("unsupported proxy protocol: %s", scheme)
	}
	if parsed.Hostname() == "" {
		return nil, fmt.Errorf("proxy must include a host: %s", raw)
	}
	port := parsed.Port()
	if port == "" {
		return nil, fmt.Errorf("proxy must include a port: %s", raw)
	}
	password, _ := parsed.User.Password()
	return &Proxy{
		Raw:    raw,
		Scheme: scheme,
		Host:   parsed.Hostname(),
		Port:   port,
		User:   parsed.User.Username(),
		Pass:   password,
	}, nil
}

func (p *Proxy) goEnvValue() string {
	if p == nil {
		return ""
	}
	if p.Scheme == "socks5h" {
		return "socks5://" + net.JoinHostPort(p.Host, p.Port)
	}
	return p.Raw
}

func newHTTPClient(proxy *Proxy, timeout time.Duration) *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext:           dialer.DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}
	if proxy != nil {
		switch proxy.Scheme {
		case "http", "https":
			proxyURL := &url.URL{Scheme: proxy.Scheme, Host: net.JoinHostPort(proxy.Host, proxy.Port)}
			if proxy.User != "" {
				proxyURL.User = url.UserPassword(proxy.User, proxy.Pass)
			}
			transport.Proxy = http.ProxyURL(proxyURL)
		case "socks5", "socks5h":
			transport.DialContext = proxy.socks5DialContext
		}
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

func (p *Proxy) socks5DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 30 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(p.Host, p.Port))
	if err != nil {
		return nil, fmt.Errorf("dial socks5 proxy: %w", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	_ = conn.SetDeadline(deadline)

	if err := p.socks5Handshake(conn, address); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func (p *Proxy) socks5Handshake(conn net.Conn, address string) error {
	methods := []byte{0x00}
	if p.User != "" {
		methods = []byte{0x00, 0x02}
	}
	greeting := append([]byte{0x05, byte(len(methods))}, methods...)
	if _, err := conn.Write(greeting); err != nil {
		return fmt.Errorf("socks5 greeting: %w", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return fmt.Errorf("socks5 greeting reply: %w", err)
	}
	if reply[0] != 0x05 {
		return fmt.Errorf("socks5: unexpected version %d", reply[0])
	}
	switch reply[1] {
	case 0x00:
		// no auth
	case 0x02:
		if p.User == "" {
			return fmt.Errorf("socks5: proxy requires username/password")
		}
		auth := []byte{0x01, byte(len(p.User))}
		auth = append(auth, p.User...)
		auth = append(auth, byte(len(p.Pass)))
		auth = append(auth, p.Pass...)
		if _, err := conn.Write(auth); err != nil {
			return fmt.Errorf("socks5 auth: %w", err)
		}
		authReply := make([]byte, 2)
		if _, err := io.ReadFull(conn, authReply); err != nil {
			return fmt.Errorf("socks5 auth reply: %w", err)
		}
		if authReply[1] != 0x00 {
			return fmt.Errorf("socks5: authentication failed")
		}
	default:
		return fmt.Errorf("socks5: unsupported auth method %d", reply[1])
	}

	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("socks5: bad address %q: %w", address, err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("socks5: bad port %q", portText)
	}

	request := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			request = append(request, 0x01)
			request = append(request, ip4...)
		} else {
			request = append(request, 0x04)
			request = append(request, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return fmt.Errorf("socks5: hostname too long")
		}
		request = append(request, 0x03, byte(len(host)))
		request = append(request, host...)
	}
	request = binary.BigEndian.AppendUint16(request, uint16(port))

	if _, err := conn.Write(request); err != nil {
		return fmt.Errorf("socks5 connect: %w", err)
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return fmt.Errorf("socks5 connect reply: %w", err)
	}
	if header[0] != 0x05 {
		return fmt.Errorf("socks5: unexpected reply version %d", header[0])
	}
	if header[1] != 0x00 {
		return fmt.Errorf("socks5: connect failed with code %d", header[1])
	}
	switch header[3] {
	case 0x01:
		_, _ = io.CopyN(io.Discard, conn, 4)
	case 0x04:
		_, _ = io.CopyN(io.Discard, conn, 16)
	case 0x03:
		length := make([]byte, 1)
		if _, err := io.ReadFull(conn, length); err != nil {
			return fmt.Errorf("socks5: read domain length: %w", err)
		}
		_, _ = io.CopyN(io.Discard, conn, int64(length[0]))
	default:
		return fmt.Errorf("socks5: unknown address type %d", header[3])
	}
	_, _ = io.CopyN(io.Discard, conn, 2) // port
	return nil
}

func goProxyEnv(proxy *Proxy) map[string]string {
	env := map[string]string{"NO_PROXY": "127.0.0.1,localhost,::1", "no_proxy": "127.0.0.1,localhost,::1"}
	if proxy == nil {
		return env
	}
	value := proxy.goEnvValue()
	env["HTTPS_PROXY"] = value
	env["HTTP_PROXY"] = value
	env["ALL_PROXY"] = value
	env["https_proxy"] = value
	env["http_proxy"] = value
	env["all_proxy"] = value
	return env
}

func gitProxyEntries(proxy *Proxy) [][2]string {
	if proxy == nil {
		return nil
	}
	return [][2]string{
		{"http.proxy", proxy.Raw},
		{"https.proxy", proxy.Raw},
	}
}
