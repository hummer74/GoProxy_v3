// http_pac.go - Оптимизированная версия генерации PAC файлов
package main

import (
	"fmt"
	"bufio"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// localProxyPortRe matches 127.0.0.1:<port> in PAC files so we can normalise
// whatever port the user wrote in proxy_pac.txt to the current ProxyPort.
var localProxyPortRe = regexp.MustCompile(`127\.0\.0\.1:\d+`)

// startPACServer generates the PAC file based on hierarchy and GeoIP
func startPACServer() {
	pacFilePath := Config.TempFiles.PACFile

	// 1. Generate content
	pacContent := generatePACContent()
	CurrentPACContent = pacContent

	// 2. Write to disk
	if err := os.WriteFile(pacFilePath, []byte(pacContent), 0644); err != nil {
		fmt.Printf("[WARN] Failed to write PAC file: %v\n", err)
		return
	}
	fmt.Printf("[OK] PAC file generated: %s\n", pacFilePath)
}

// generatePACContent builds the PAC with 5 clearly separated sections:
//   Section 1 — Custom domain rules (proxy_pac.txt)
//   Section 2 — Hardcoded domain fallback rules (Optimized via Regex)
//   Section 3 — GeoIP Russia → DIRECT
//   Section 4 — Private IP safety net → DIRECT
//   Section 5 — Default proxy for everything else
func generatePACContent() string {
	var sb strings.Builder

	// ── Header & Section 0: System Exceptions ──
	sb.WriteString(`function FindProxyForURL(url, host) {
    host = host.toLowerCase();

    if (isPlainHostName(host) || host === "localhost") {
        return "DIRECT";
    }

`)

	// ── Section 1: Custom Rules (highest priority) ──
	customBody := loadAndCleanCustomRules()
	if customBody != "" {
		sb.WriteString("    // Section 1: Custom Rules (proxy_pac.txt)\n")
		sb.WriteString(customBody)
		sb.WriteString("\n\n")
	}

	// ── Section 2: Integrated Hardcode Rules (Optimized via Regex) ──
	sb.WriteString("    // Section 2: Integrated Hardcode Rules (Regex Optimized)\n")
	sb.WriteString(generateOptimizedHardcodeBlock())
	sb.WriteString("\n\n")

	// ── Section 3: GeoIP (Russia) ──
	sb.WriteString("    // Section 3: GeoIP (Russia)\n")
	sb.WriteString(generateGeoIPBlock()) // var ip = dnsResolve(host);
	sb.WriteString("\n")

	// ── Section 4: Private IP Safety Net ──
	// Reuses var ip from Section 3 (JS var is function-scoped)
	sb.WriteString("    // Section 4: Private IP Safety Net\n")
	sb.WriteString("    if (ip) {\n")
	sb.WriteString("        if (isInNet(ip, \"10.0.0.0\", \"255.0.0.0\") ||\n")
	sb.WriteString("            isInNet(ip, \"172.16.0.0\", \"255.240.0.0\") ||\n")
	sb.WriteString("            isInNet(ip, \"192.168.0.0\", \"255.255.0.0\") ||\n")
	sb.WriteString("            isInNet(ip, \"127.0.0.0\", \"255.0.0.0\") ||\n")
	sb.WriteString("            isInNet(ip, \"169.254.0.0\", \"255.255.0.0\")) {\n")
	sb.WriteString("            return \"DIRECT\";\n")
	sb.WriteString("        }\n")
	sb.WriteString("    }\n\n")

	// ── Section 5: Default Proxy ──
	sb.WriteString("    // Section 5: Default Proxy\n")
	sb.WriteString(fmt.Sprintf("    return \"SOCKS5 127.0.0.1:%d; SOCKS 127.0.0.1:%d; DIRECT\";\n}",
		Config.Network.ProxyPort, Config.Network.ProxyPort))

	return sb.String()
}

// loadAndCleanCustomRules reads proxy_pac.txt and returns ONLY the rule
// blocks (if/return) with proper indentation.
func loadAndCleanCustomRules() string {
	path := Config.Paths.PACRules
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}

	content := string(data)

	// 1. Normalise proxy port
	newProxy := fmt.Sprintf("127.0.0.1:%d", Config.Network.ProxyPort)
	content = localProxyPortRe.ReplaceAllString(content, newProxy)

	// 2. Line-by-line state machine to strip boilerplate
	type state int
	const (
		sNormal state = iota // accepting lines
		sSkipBlock           // inside a boilerplate block, skip until brace depth returns to 0
	)
	st := sNormal
	skipDepth := 0
	blockDepth := 0 // tracks depth of valid (non-skipped) blocks
	var lines []string

	for _, raw := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(raw)

		opens := strings.Count(raw, "{")
		closes := strings.Count(raw, "}")

		switch st {
		case sSkipBlock:
			skipDepth += opens - closes
			if skipDepth <= 0 {
				st = sNormal
				skipDepth = 0
			}
			continue

		case sNormal:
			// Check if this line starts a boilerplate block
			if isBoilerplateLine(trimmed) {
				if opens > 0 {
					skipDepth = opens - closes
					if skipDepth > 0 {
						st = sSkipBlock
						continue
					}
				}
				continue
			}

			// Skip bare return statements ONLY at block depth 0 (not inside an if)
			if blockDepth == 0 && opens == 0 && isBareReturn(trimmed) {
				continue
			}

			// Skip orphan closing braces
			if trimmed == "}" && blockDepth+opens-closes < 0 {
				continue
			}

			lines = append(lines, raw)
			blockDepth += opens - closes
		}
	}

	// 3. Remove empty if-blocks from collected lines
	lines = removeEmptyIfBlocks(lines)

	// 4. Indent and collapse blanks
	var result []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(result) > 0 && result[len(result)-1] != "" {
				result = append(result, "")
			}
			continue
		}
		// Skip comments referencing stripped concepts
		if strings.HasPrefix(trimmed, "//") && isStrippedComment(trimmed) {
			continue
		}
		result = append(result, "    "+trimmed)
	}

	clean := strings.Join(result, "\n")
	clean = strings.Trim(clean, " \t\n")
	if clean == "" {
		return ""
	}

	fmt.Printf("[INFO] Custom rules loaded from %s\n", path)
	return clean
}

// isBoilerplateLine returns true for lines that duplicate Section 0 logic
func isBoilerplateLine(line string) bool {
	switch {
	case strings.HasPrefix(line, "function ") && strings.Contains(line, "FindProxyForURL"):
		return true
	case strings.HasPrefix(line, "host") && strings.Contains(line, "toLowerCase"):
		return true
	case strings.HasPrefix(line, "if") && (strings.Contains(line, "isPlainHostName") ||
		strings.Contains(line, `host === "localhost"`) ||
		strings.Contains(line, `host=="localhost"`)):
		return true
	case strings.HasPrefix(line, "var ") && strings.Contains(line, "dnsResolve"):
		return true
	}
	return false
}

// isBareReturn returns true for a return statement not inside an if-block.
func isBareReturn(line string) bool {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "return ") && !strings.HasPrefix(trimmed, "return\"") {
		return false
	}
	return strings.Contains(trimmed, "PROXY") || strings.Contains(trimmed, "DIRECT") ||
		strings.Contains(trimmed, "SOCKS")
}

// isStrippedComment returns true for comments that reference stripped boilerplate.
func isStrippedComment(line string) bool {
	keywords := []string{"toLowerCase", "localhost", "loopback", "safety net",
		"fallback chain", "proxy chain", "catch remaining",
		"private ip", "default proxy", "also in pac template"}
	for _, kw := range keywords {
		if strings.Contains(line, kw) {
			return true
		}
	}
	return false
}

// removeEmptyIfBlocks scans collected lines and removes any if-block
// whose body contains no return statement.
func removeEmptyIfBlocks(lines []string) []string {
	var out []string
	i := 0
	for i < len(lines) {
		trimmed := strings.TrimSpace(lines[i])

		if strings.HasPrefix(trimmed, "if") {
			foundOpen := false
			j := i
			for j < len(lines) {
				if strings.Contains(lines[j], "{") {
					foundOpen = true
					break
				}
				if j > i+50 {
					break
				}
				j++
			}

			if !foundOpen {
				out = append(out, lines[i])
				i++
				continue
			}

			depth := strings.Count(lines[j], "{") - strings.Count(lines[j], "}")
			bodyStart := j + 1
			k := bodyStart
			body := ""
			for k < len(lines) && depth > 0 {
				body += lines[k] + "\n"
				depth += strings.Count(lines[k], "{") - strings.Count(lines[k], "}")
				k++
			}

			fullBlock := ""
			for x := i; x < k; x++ {
				fullBlock += lines[x] + "\n"
			}

			if !strings.Contains(fullBlock, "return") {
				i = k
				continue
			}
		}

		out = append(out, lines[i])
		i++
	}
	return out
}

// generateOptimizedHardcodeBlock replaces manual shExpMatch chains with RegExp logic.
// This significantly improves browser performance and reduces PAC file size.
func generateOptimizedHardcodeBlock() string {
	proxyStr := fmt.Sprintf("SOCKS5 127.0.0.1:%d; SOCKS 127.0.0.1:%d", Config.Network.ProxyPort, Config.Network.ProxyPort)

	// Regex for Russian & local zones (DIRECT)
	// Combines: TLDs (.ru, .su, .рф), Prefixes (max.*, ya.*), Subdomains (*.vk.*)
	directRegex := `^(max\.|apptracer\.|oneme\.)|(ya\.|yandex\.|pingzen\.|vk\.|vkontakte\.)(.*)|\.(ru|su|r[ф]f|x?xn--p1ai|team|company)$`

	// Regex for Google / YouTube (SOCKS)
	googleRegex := `(google\.|youtube\.|ytimg\.)|(googleapis\.|gstatic\.|ggpht\.)`

	return fmt.Sprintf(`    if (/%s/.test(host)) { return "DIRECT"; }`, directRegex) + "\n" + 
		   fmt.Sprintf(`    if (/%s/.test(host)) { return "%s; DIRECT"; }`, googleRegex, proxyStr)
}

// generateGeoIPBlock builds the GeoIP JavaScript block with nested 2nd-octet switch for large first-octet groups.
func generateGeoIPBlock() string {
	cidrs := getOrFetchGeoIP()
	if len(cidrs) == 0 {
		return ""
	}

	type geoEntry struct {
		ip        string
		prefix    int
		mask      string
		firstOct  int
		secondOct int
	}

	entries := make([]geoEntry, 0, len(cidrs))
	for _, c := range cidrs {
		parts := strings.Split(c.IP, ".")
		if len(parts) != 4 {
			continue
		}
		f, _ := strconv.Atoi(parts[0])
		s, _ := strconv.Atoi(parts[1])
		p, _ := strconv.Atoi(c.Prefix)
		entries = append(entries, geoEntry{
			ip:        c.IP,
			prefix:    p,
			mask:      c.Mask,
			firstOct:  f,
			secondOct: s,
		})
	}

	groups := make(map[int][]geoEntry)
	for _, e := range entries {
		groups[e.firstOct] = append(groups[e.firstOct], e)
	}

	var firstKeys []int
	for k := range groups {
		firstKeys = append(firstKeys, k)
	}
	sort.Ints(firstKeys)

	const subSwitchThreshold = 5

	var js strings.Builder
	js.WriteString("    var ip = dnsResolve(host);\n    if (ip) {\n        var f = ip.split('.')[0];\n        switch(f) {\n")

	for _, fk := range firstKeys {
		group := groups[fk]

		if len(group) >= subSwitchThreshold {
			js.WriteString(fmt.Sprintf("            case '%d':\n                var s = ip.split('.')[1];\n                switch(s) {\n", fk))

			subGroups := make(map[int][]geoEntry)
			for _, e := range group {
				subGroups[e.secondOct] = append(subGroups[e.secondOct], e)
			}
			var subKeys []int
			for k := range subGroups {
				subKeys = append(subKeys, k)
			}
			sort.Ints(subKeys)

			for _, sk := range subKeys {
				sub := subGroups[sk]
				sort.Slice(sub, func(i, j int) bool {
					return sub[i].prefix < sub[j].prefix
				})
				js.WriteString(fmt.Sprintf("                    case '%d':\n", sk))
				for _, e := range sub {
					js.WriteString(fmt.Sprintf("                        if(isInNet(ip,'%s','%s'))return 'DIRECT';\n",
						e.ip, e.mask))
				}
				js.WriteString("                        break;\n")
			}

			js.WriteString("                }\n                break;\n")
		} else {
			sort.Slice(group, func(i, j int) bool {
				return group[i].prefix < group[j].prefix
			})
			js.WriteString(fmt.Sprintf("            case '%d':\n", fk))
			for _, e := range group {
				js.WriteString(fmt.Sprintf("                if(isInNet(ip,'%s','%s'))return 'DIRECT';\n",
					e.ip, e.mask))
			}
			js.WriteString("                break;\n")
		}
	}

	js.WriteString("        }\n    }")

	fmt.Printf("[INFO] GeoIP: %d entries, %d first-octet groups, nested 2nd-octet for groups ≥ %d\n",
		len(entries), len(firstKeys), subSwitchThreshold)

	return js.String()
}

// getOrFetchGeoIP loads cached GeoIP data or fetches from ipdeny.com
func getOrFetchGeoIP() []CIDRItem {
	sshDir := filepath.Join(Config.Paths.WorkDir, ".ssh")
	cachePath := filepath.Join(sshDir, "geoip_ru.txt")

	if info, err := os.Stat(sshDir); err != nil || !info.IsDir() {
		fmt.Printf("[WARN] Directory %s not found. GeoIP caching disabled.\n", sshDir)
	}

	if info, err := os.Stat(cachePath); err == nil && time.Since(info.ModTime()) < 24*time.Hour {
		d, _ := os.ReadFile(cachePath)
		return parseCIDR(string(d))
	}

	resp, err := http.Get("http://www.ipdeny.com/ipblocks/data/aggregated/ru-aggregated.zone")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var b strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		b.WriteString(scanner.Text() + "\n")
	}

	if _, err := os.Stat(sshDir); err == nil {
		_ = os.WriteFile(cachePath, []byte(b.String()), 0644)
	}
	return parseCIDR(b.String())
}

// parseCIDR parses "IP/prefix" lines into CIDRItem slice
func parseCIDR(data string) []CIDRItem {
	var res []CIDRItem
	for _, line := range strings.Split(data, "\n") {
		p := strings.Split(strings.TrimSpace(line), "/")
		if len(p) == 2 {
			mask := cidrPrefixToNetmask(p[1])
			if mask != "" {
				res = append(res, CIDRItem{IP: p[0], Mask: mask, Prefix: p[1]})
			}
		}
	}
	return res
}

// cidrPrefixToNetmask converts a CIDR prefix (e.g. "19") to dotted-decimal netmask
func cidrPrefixToNetmask(prefix string) string {
	p, _ := strconv.Atoi(prefix)
	if p < 0 || p > 32 {
		return ""
	}
	var m uint32
	if p > 0 {
		m = 0xFFFFFFFF << (32 - p)
	}
	return fmt.Sprintf("%d.%d.%d.%d", byte(m>>24), byte(m>>16), byte(m>>8), byte(m))
}

// CIDRItem represents a single CIDR range with both netmask and prefix
type CIDRItem struct {
	IP, Mask, Prefix string
}
