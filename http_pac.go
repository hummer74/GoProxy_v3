package main

import (
        "bufio"
        "fmt"
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

// generatePACContent builds the PAC using 5-stage logic
func generatePACContent() string {
        var sb strings.Builder

        // Header & Section 0: System Exceptions
        sb.WriteString(`function FindProxyForURL(url, host) {
    host = host.toLowerCase();

    if (isPlainHostName(host) || host === "localhost") {
        return "DIRECT";
    }

`)

        // Section 1: Custom Rules (Priority)
        customBody := loadAndCleanCustomRules()
        if customBody != "" {
                sb.WriteString("    // Section 1: Custom Rules (proxy_pac.txt)\n")
                sb.WriteString(customBody)
                sb.WriteString("\n\n")
        } else {
                // Section 2: Integrated Hardcode
                sb.WriteString("    // Section 2: Integrated Hardcode Rules\n")
                sb.WriteString(generateHardcodeBlock())
                sb.WriteString("\n\n")
        }

        // Section 3: GeoIP (Russia) with Sorted Octets
        sb.WriteString("    // Section 3: GeoIP (Russia)\n")
        sb.WriteString(generateGeoIPBlock())
        sb.WriteString("\n\n")

        // Section 4 & 5: Safety Net and Fallback
        sb.WriteString(fmt.Sprintf(`    // Section 4 & 5: Safety Net & Final Fallback
    var resolved_ip = dnsResolve(host);
    if (resolved_ip) {
        if (isInNet(resolved_ip, "10.0.0.0", "255.0.0.0") ||
            isInNet(resolved_ip, "172.16.0.0", "255.240.0.0") ||
            isInNet(resolved_ip, "192.168.0.0", "255.255.0.0") ||
            isInNet(resolved_ip, "127.0.0.0", "255.0.0.0") ||
            isInNet(resolved_ip, "169.254.0.0", "255.255.0.0")) {
            return "DIRECT";
        }
    }
    return "SOCKS5 127.0.0.1:%d; SOCKS 127.0.0.1:%d; DIRECT";
}`, Config.Network.ProxyPort, Config.Network.ProxyPort))

        return sb.String()
}

func loadAndCleanCustomRules() string {
        path := filepath.Join(Config.Paths.WorkDir, "proxy_pac.txt")
        data, err := os.ReadFile(path)
        if err != nil { return "" }

        content := string(data)
        newProxy := fmt.Sprintf("127.0.0.1:%d", Config.Network.ProxyPort)
        content = localProxyPortRe.ReplaceAllString(content, newProxy)

        first := strings.Index(content, "{")
        last := strings.LastIndex(content, "}")
        if first != -1 && last != -1 && last > first {
                inner := content[first+1 : last]
                lines := strings.Split(inner, "\n")
                var result []string
                for _, line := range lines {
                        trimmed := strings.TrimSpace(line)
                        if strings.HasPrefix(trimmed, "return \"SOCKS") && !strings.Contains(trimmed, "if") {
                                continue 
                        }
                        if trimmed != "" { result = append(result, "    "+line) }
                }
                return strings.Join(result, "\n")
        }
        return ""
}

func generateHardcodeBlock() string {
        proxyStr := fmt.Sprintf("return \"SOCKS5 127.0.0.1:%d; DIRECT\";", Config.Network.ProxyPort)
        return fmt.Sprintf(`    if (shExpMatch(host, "*.google.*") || shExpMatch(host, "google.*") ||
        shExpMatch(host, "*.googleapis.*") || shExpMatch(host, "*.gstatic.*") ||
        shExpMatch(host, "*.ggpht.*") || shExpMatch(host, "*.youtube.*") || shExpMatch(host, "*.ytimg.*")) {
        %s
    }
    if (shExpMatch(host, "*.ru") || shExpMatch(host, "*.su") || shExpMatch(host, "*.рф") ||
        shExpMatch(host, "*.xn--p1ai") || shExpMatch(host, "*.local") || shExpMatch(host, "*.team") ||
        shExpMatch(host, "*.company") || shExpMatch(host, "max.*") || shExpMatch(host, "*.max.*") ||
        shExpMatch(host, "*.apptracer.*") || shExpMatch(host, "*.oneme.*") ||
        shExpMatch(host, "vk.*") || shExpMatch(host, "*.vk.*") || shExpMatch(host, "vkontakte.*") ||
        shExpMatch(host, "*.vkontakte.*") || shExpMatch(host, "ya.*") || shExpMatch(host, "*.ya.*") ||
        shExpMatch(host, "yandex.*") || shExpMatch(host, "*.yandex.*") ||
        shExpMatch(host, "yastatic.*") || shExpMatch(host, "*.yastatic.*") ||
        shExpMatch(host, "azimut-ural.*") || shExpMatch(host, "*.azimut-ural.*") ||
        shExpMatch(host, "2gis.*") || shExpMatch(host, "*.2gis.*") ||
        shExpMatch(host, "mail.ru") || shExpMatch(host, "*.mail.*")) {
        return "DIRECT";
    }`, proxyStr)
}

func generateGeoIPBlock() string {
        cidrs := getOrFetchGeoIP()
        if len(cidrs) == 0 { return "" }

        groups := make(map[string][]string)
        for _, c := range cidrs {
                parts := strings.Split(c.IP, ".")
                if len(parts) > 0 {
                        groups[parts[0]] = append(groups[parts[0]], 
                                fmt.Sprintf("if(isInNet(ip,'%s','%s'))return 'DIRECT';", c.IP, c.Mask))
                }
        }

        // SORTING first octets (keys) numerically
        var keys []int
        for k := range groups {
                val, _ := strconv.Atoi(k)
                keys = append(keys, val)
        }
        sort.Ints(keys)

        var js strings.Builder
        js.WriteString("    var ip = dnsResolve(host);\n    if (ip) {\n        var f = ip.split('.')[0];\n        switch(f) {\n")
        for _, k := range keys {
                octetStr := strconv.Itoa(k)
                js.WriteString(fmt.Sprintf("            case '%s':\n                %s\n                break;\n", 
                        octetStr, strings.Join(groups[octetStr], " ")))
        }
        js.WriteString("        }\n    }")
        return js.String()
}

func getOrFetchGeoIP() []CIDRItem {
        sshDir := filepath.Join(Config.Paths.WorkDir, ".ssh")
        cachePath := filepath.Join(sshDir, "geoip_ru.txt")
        
        // Check directory existence
        if info, err := os.Stat(sshDir); err != nil || !info.IsDir() {
                fmt.Printf("[WARN] Directory %s not found. GeoIP caching disabled.\n", sshDir)
                // We can still try to download and use in-memory but won't save
        }

        if info, err := os.Stat(cachePath); err == nil && time.Since(info.ModTime()) < 24*time.Hour {
                d, _ := os.ReadFile(cachePath)
                return parseCIDR(string(d))
        }

        resp, err := http.Get("http://www.ipdeny.com/ipblocks/data/aggregated/ru-aggregated.zone")
        if err != nil { return nil }
        defer resp.Body.Close()
        
        var b strings.Builder
        scanner := bufio.NewScanner(resp.Body)
        for scanner.Scan() { b.WriteString(scanner.Text() + "\n") }
        
        // Save only if directory exists
        if _, err := os.Stat(sshDir); err == nil {
                _ = os.WriteFile(cachePath, []byte(b.String()), 0644)
        }
        return parseCIDR(b.String())
}

func parseCIDR(data string) []CIDRItem {
        var res []CIDRItem
        for _, line := range strings.Split(data, "\n") {
                p := strings.Split(strings.TrimSpace(line), "/")
                if len(p) == 2 {
                        mask := cidrPrefixToNetmask(p[1])
                        if mask != "" { res = append(res, CIDRItem{IP: p[0], Mask: mask}) }
                }
        }
        return res
}

func cidrPrefixToNetmask(prefix string) string {
        p, _ := strconv.Atoi(prefix)
        if p < 0 || p > 32 { return "" }
        var m uint32
        if p > 0 { m = 0xFFFFFFFF << (32 - p) }
        return fmt.Sprintf("%d.%d.%d.%d", byte(m>>24), byte(m>>16), byte(m>>8), byte(m))
}

type CIDRItem struct { IP, Mask string }