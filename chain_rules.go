// chain_rules.go — правила цепочек с недоступными напрямую хостами.
//
// Термины:
//
//	direct status — результат ПРЯМОЙ проверки хоста с этой машины (hostStatusCache).
//	Хосты без прямого доступа помечаются ○ и доступны только как 2-й+ хоп цепочки.
//	Одиночное подключение таких хостов запрещено (клик показывает подсказку).
package main

import (
	"fmt"
	"time"
)

// ChainOnlyMarker prefixes menu titles of hosts without direct access.
const ChainOnlyMarker = "○"

// SocksFailThreshold — consecutive SOCKS failures before leaving Normal.
const SocksFailThreshold = 3

// MaxFailoverDuration — give up the failover loop and go ErrorHalt/NoInternet.
const MaxFailoverDuration = 5 * time.Minute

// NoInternetRedAfter — grace period with yellow "waiting" before the red icon.
const NoInternetRedAfter = 3 * time.Minute

// ErrorHaltRetryAfter — how long a retryable ErrorHalt waits before trying
// recovery instead of wedging red forever. Manual halts never retry.
const ErrorHaltRetryAfter = 5 * time.Minute

// UplinkCheckInterval — how often to prove real traffic flows through the tunnel.
const UplinkCheckInterval = 60 * time.Second

// Preflight test ports (offsets from ProxyPort) — each flow gets its own
// so parallel probes never steal each other's listeners.
const (
	preflightPortFailover      = 1000
	preflightPortChainRecovery = 1001
	preflightPortRestore       = 1002
	preflightPortUserChain     = 1003
	preflightPortChainRefail   = 1004
)

// chainOnlyTitle builds the menu title for a host without direct access.
func chainOnlyTitle(paddedName string) string {
	return fmt.Sprintf("%s %s", ChainOnlyMarker, paddedName)
}

// chainOnlyTooltip explains why a host is chain-only.
func chainOnlyTooltip(hostName string) string {
	return fmt.Sprintf("%s: no direct access — chain only (add as 2nd+ hop). Single connect unavailable.", hostName)
}

// directStatus returns cached DIRECT reachability (unknown when unchecked yet).
func directStatus(name string) (bool, bool) {
	if hostStatusCache == nil {
		return false, false
	}
	return hostStatusCache.Get(name)
}

// classifyChain validates builder content structurally.
// Returns "single" or "chain". Unknown first-hop status is allowed
// (the hard ssh probe at Connect decides); a known-unreachable first hop
// is rejected; an empty builder is rejected.
func classifyChain(chain []HostConfig) (string, error) {
	if len(chain) == 0 {
		return "", fmt.Errorf("chain is empty")
	}
	if len(chain) == 1 {
		return "single", nil
	}
	if status, known := directStatus(chain[0].Name); known && !status {
		return "", fmt.Errorf("first hop %q has no direct access — put a reachable host first", chain[0].Name)
	}
	return "chain", nil
}

// connectButtonState decides the Connect button from the builder snapshot.
// Pure (besides the status cache) — covered by unit tests.
func connectButtonState(chainCopy []HostConfig) (enabled bool, title, tooltip string) {
	if len(chainCopy) == 0 {
		return false,
			fmt.Sprintf("%s Connect Chain", ChainArrow),
			"Select hosts then click to connect"
	}
	var chainNames []string
	for i, ch := range chainCopy {
		chainNames = append(chainNames, fmt.Sprintf("[%d] %s", i+1, ch.Name))
	}
	chainDisplay := joinChainNames(chainNames)
	if _, err := classifyChain(chainCopy); err != nil {
		return false,
			fmt.Sprintf("%s Connect Chain (%d hosts)", ChainArrow, len(chainCopy)),
			fmt.Sprintf("Blocked: %s", err.Error())
	}
	if len(chainCopy) == 1 {
		if status, known := directStatus(chainCopy[0].Name); known && !status {
			return false,
				fmt.Sprintf("%s Connect (%d host)", ChainArrow, len(chainCopy)),
				fmt.Sprintf("Blocked: %s has no direct access — single connect unavailable.", chainCopy[0].Name)
		}
		return true,
			fmt.Sprintf("%s Connect (%d host)", ChainArrow, len(chainCopy)),
			fmt.Sprintf("Connect: %s", chainDisplay)
	}
	return true,
		fmt.Sprintf("%s Connect Chain (%d hosts)", ChainArrow, len(chainCopy)),
		fmt.Sprintf("Connect: %s", chainDisplay)
}

// chainDisplayOf formats "[a] -> [b]" member names for logs.
func chainDisplayOf(chain []HostConfig) string {
	names := make([]string, 0, len(chain))
	for _, h := range chain {
		names = append(names, h.Name)
	}
	return joinChainNames(names)
}
func joinChainNames(chainNames []string) string {
	out := ""
	for i, n := range chainNames {
		if i > 0 {
			out += " -> "
		}
		out += n
	}
	return out
}

// firstHopProbeFn does a fresh ssh-level reachability check of one host
// (real ssh.exe, not just TCP — aliases/ProxyJump-aware). Mockable in tests.
var firstHopProbeFn = func(h HostConfig) bool {
	if Config == nil {
		return false
	}
	ok, _ := checkSSHConnectionWithTime(h, Config.Paths.WorkDir)
	return ok
}

// recordSocks feeds one SOCKS probe result into the debounce counter.
// Returns true when the failover threshold is reached.
func (mc *monitoringConfig) recordSocks(ok bool) bool {
	if ok {
		mc.socksFails = 0
		return false
	}
	mc.socksFails++
	return mc.socksFails >= SocksFailThreshold
}

// failoverExpired reports whether the failover loop ran too long.
func failoverExpired(start, now time.Time) bool {
	if start.IsZero() {
		return false
	}
	return now.Sub(start) >= MaxFailoverDuration
}

// noInternetShouldBeRed reports whether the NoInternet grace period is over.
func noInternetShouldBeRed(start, now time.Time) bool {
	if start.IsZero() {
		return false
	}
	return now.Sub(start) >= NoInternetRedAfter
}

// preflightTimeout scales the tunnel probe timeout with chain length.
func preflightTimeout(chainLen int) time.Duration {
	d := 15 * time.Duration(chainLen) * time.Second
	if d < 15*time.Second {
		d = 15 * time.Second
	}
	if d > 45*time.Second {
		d = 45 * time.Second
	}
	return d
}

// sleepGapExceeded reports a monitor tick gap big enough to mean sleep,
// hibernation or a clock jump: in-flight probes from before the gap are
// meaningless, so the caller resets debounce and skips one tick.
func sleepGapExceeded(last, now time.Time) bool {
	if last.IsZero() {
		return false
	}
	return now.Sub(last) > 30*time.Second
}

// UplinkFailThreshold.
const UplinkFailThreshold = 3

// checkUplinkFunc probes end-to-end traffic.
var checkUplinkFunc = checkInternetViaProxy

// recordUplink feeds one end-to-end probe result into the debounce counter.
func (mc *monitoringConfig) recordUplink(ok bool) bool {
	if ok {
		mc.uplinkFails = 0
		return false
	}
	mc.uplinkFails++
	return mc.uplinkFails >= UplinkFailThreshold
}

// autoConnectCandidates filters direct hosts of the priority group for
// auto-connect fallback: no reverse, no jumper, no ProxyJump-only hosts.
// Pure, no network - unit-testable.
func autoConnectCandidates(hosts []HostConfig, exclude string) []HostConfig {
	if len(hosts) == 0 {
		return nil
	}
	group := hosts[0].Group
	var out []HostConfig
	for _, h := range hosts {
		if h.Group != group || h.Name == exclude {
			continue
		}
		if isReverseHost(h) || isReverseJumperHost(h) || h.ProxyJump != "" {
			continue
		}
		out = append(out, h)
	}
	return out
}
