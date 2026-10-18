package ingest

import (
	"errors"
	"net"
	"net/url"
)

func AssertPublicHTTPURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return errors.New("invalid URL")
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return errors.New("only http(s) URLs are supported")
	}

	host := parsed.Hostname()
	if host == "" {
		return errors.New("URL has no host")
	}

	ips, err := net.LookupIP(host)
	if err != nil {
		return errors.New("could not resolve the URL's host")
	}

	for _, ip := range ips {
		if isPrivateOrReservedIP(ip) {
			return errors.New("URL points to a private or internal address")
		}
	}

	return nil
}

func isPrivateOrReservedIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}

	// Also check IPv4 0.0.0.0/8 or Carrier-grade NAT 100.64.0.0/10
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 0 {
			return true
		}
		if ip4[0] == 100 && (ip4[1]&0xc0) == 64 {
			return true
		}
	}
	return false
}
