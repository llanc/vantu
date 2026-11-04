package orchestrator

import (
	"fmt"
	"net/netip"
	"net/textproto"
	"regexp"
	"strings"

	"github.com/llanc/vantu/internal/store"
)

const (
	maxIPAllowEntries = 64
	maxCustomHeaders  = 32
)

var headerNameRe = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")

var reservedRequestHeaders = map[string]bool{
	"Host":              true,
	"X-Vantu-Route":     true,
	"X-Vantu-Relay-Key": true,
	"Connection":        true,
	"Content-Length":    true,
	"Transfer-Encoding": true,
	"Upgrade":           true,
}

var reservedResponseHeaders = map[string]bool{
	"Connection":        true,
	"Content-Length":    true,
	"Transfer-Encoding": true,
}

// normalizeProxyOptions validates user supplied proxy options and returns a canonical copy.
func normalizeProxyOptions(p store.ProxyOptions) (store.ProxyOptions, error) {
	out := store.ProxyOptions{
		TLSSkipVerify: p.TLSSkipVerify,
		Compress:      p.Compress,
	}

	host := strings.TrimSpace(p.UpstreamHost)
	if host != "" {
		if len(host) > 253 || strings.ContainsAny(host, " /\\\t\r\n") || strings.Contains(host, "://") {
			return out, newAppErr("VALIDATION_ERROR", fmt.Sprintf("上游 Host %q 无效", host), "只填写主机名，可带端口，例如 app.internal 或 app.internal:8080", 400, 2)
		}
		out.UpstreamHost = host
	}

	seen := map[string]bool{}
	for _, raw := range p.IPAllowlist {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		var canon string
		if pfx, err := netip.ParsePrefix(entry); err == nil {
			canon = pfx.Masked().String()
		} else if addr, err := netip.ParseAddr(entry); err == nil {
			canon = addr.String()
		} else {
			return out, newAppErr("VALIDATION_ERROR", fmt.Sprintf("IP 白名单条目 %q 无效", entry), "请填写 IP 或 CIDR 网段，例如 203.0.113.7 或 10.0.0.0/8", 400, 2)
		}
		if seen[canon] {
			continue
		}
		seen[canon] = true
		out.IPAllowlist = append(out.IPAllowlist, canon)
	}
	if len(out.IPAllowlist) > maxIPAllowEntries {
		return out, newAppErr("VALIDATION_ERROR", fmt.Sprintf("IP 白名单最多 %d 条", maxIPAllowEntries), "", 400, 2)
	}

	var err error
	if out.RequestHeaders, err = normalizeHeaders(p.RequestHeaders, reservedRequestHeaders); err != nil {
		return out, err
	}
	if out.ResponseHeaders, err = normalizeHeaders(p.ResponseHeaders, reservedResponseHeaders); err != nil {
		return out, err
	}
	return out, nil
}

func normalizeHeaders(in map[string]string, reserved map[string]bool) (map[string]string, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > maxCustomHeaders {
		return nil, newAppErr("VALIDATION_ERROR", fmt.Sprintf("自定义头最多 %d 个", maxCustomHeaders), "", 400, 2)
	}
	out := make(map[string]string, len(in))
	for name, value := range in {
		name = strings.TrimSpace(name)
		if !headerNameRe.MatchString(name) {
			return nil, newAppErr("VALIDATION_ERROR", fmt.Sprintf("自定义头名称 %q 无效", name), "", 400, 2)
		}
		canon := textproto.CanonicalMIMEHeaderKey(name)
		if reserved[canon] {
			return nil, newAppErr("VALIDATION_ERROR", fmt.Sprintf("%s 头由小凡兔管理，不能自定义", canon), "修改上游 Host 请使用“上游 Host”选项", 400, 2)
		}
		if strings.ContainsAny(value, "\r\n") {
			return nil, newAppErr("VALIDATION_ERROR", fmt.Sprintf("%s 头的值不能包含换行", canon), "", 400, 2)
		}
		out[canon] = strings.TrimSpace(value)
	}
	return out, nil
}

// errIPAllowWithCDN guards against allowlists that would only ever see Cloudflare's addresses.
func errIPAllowWithCDN() error {
	return newAppErr("VALIDATION_ERROR", "IP 白名单不能与 CDN 代理同时开启", "开启 CDN 后边缘节点看到的是 Cloudflare 的 IP，请先关闭 CDN 代理或清空白名单", 400, 2)
}
