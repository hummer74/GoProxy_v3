package main

import (
	"os"
	"testing"
	"time"
)

func TestStopMonitoring_WhenActive_SendsStopSignalAndDeactivates(t *testing.T) {
	monitoringStopChan = make(chan bool, 1)
	monitoringActive = true

	stopMonitoring()

	if monitoringActive {
		t.Fatal("expected monitoringActive to be false after stopMonitoring")
	}

	select {
	case v := <-monitoringStopChan:
		if !v {
			t.Fatal("expected stop signal value true")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected stop signal to be sent to monitoringStopChan")
	}
}

func TestStopMonitoring_WhenInactive_DoesNotSendSignal(t *testing.T) {
	monitoringStopChan = make(chan bool, 1)
	monitoringActive = false

	stopMonitoring()

	select {
	case <-monitoringStopChan:
		t.Fatal("did not expect a stop signal when monitoringActive is false")
	case <-time.After(100 * time.Millisecond):
		// ok
	}
}

func TestHandleChainConnection_EmptyChain_NoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("expected no panic for empty chain, got %v", r)
		}
	}()

	handleChainConnection(nil, true)
}

func TestHandleChainConnection_AlreadyConnected_DoesNothing(t *testing.T) {
	isTunnelActive = true
	currentHost = "a -> b"

	chain := []HostConfig{{Name: "a"}, {Name: "b"}}

	handleChainConnection(chain, true)

	if !isTunnelActive || currentHost != "a -> b" {
		t.Fatalf("expected no state change when already connected: got isTunnelActive=%v currentHost=%q", isTunnelActive, currentHost)
	}
}

func TestStartMonitoring_OriginalHostReturnResetsMonitoringActive(t *testing.T) {
	// Setup minimal config for fast timer trigger
	Config = &AppConfig{}
	Config.Network.SocksCheckInterval = 1
	Config.Network.InternetCheckDelay = 1
	Config.Network.InternetCheckRetry = 1
	Config.Network.ReconnectAttemptDelay = 1
	Config.Network.MaxReconnectTime = 1
	Config.General.ReturnToOriginalHost = true
	Config.General.OriginalHostCheck = 0

	monitoringStopChan = make(chan bool, 1)
	monitoringActive = false

	state := &ProxyState{IsFailoverActive: true}

	origReturnFunc := checkAndReturnToOriginalHostFunc
	origCheckProxyFunc := checkProxyConnectivityFunc
	checkAndReturnToOriginalHostFunc = func(*ProxyState) bool { return true }
	checkProxyConnectivityFunc = func() bool { return true }
	defer func() {
		checkAndReturnToOriginalHostFunc = origReturnFunc
		checkProxyConnectivityFunc = origCheckProxyFunc
	}()

	startMonitoring(state)

	if monitoringActive {
		t.Fatal("expected monitoringActive to be false after original host return path")
	}
}

func TestHandleConnectionInteractive_SetTunnelActive(t *testing.T) {
	// Setup config
	cfgDir := t.TempDir()
	Config = &AppConfig{
		Network:   AppConfig{}.Network,
		Paths:     AppConfig{}.Paths,
		TempFiles: AppConfig{}.TempFiles,
	}
	Config.Network.ProxyPort = 1080
	Config.Network.PACHttpPort = 8080
	Config.Paths.WorkDir = cfgDir
	Config.TempFiles.SSHTunnelPID = cfgDir + "\\ssh_tunnel.pid"

	// Set hooks
	startSSHTunnelFn = func(_ []string) bool { return true }
	startSSHTunnelWithRetriesFn = func(_ []string, _ int) bool { return true }
	launchTrayAndExitFn = func() bool { return true }
	startPACServerFn = func() {}
	setSystemProxyFn = func(_ string) bool { return true }
	ensureSSHAgentFn = func(_, _ string) bool { return true }
	saveStateFn = func(_ ProxyState) error { return nil }
	saveLastHostFn = func(_ string) error { return nil }
	startMonitoringFn = func(_ *ProxyState) {}
	stopMonitoringFn = func() {}
	killProcessByFileFn = func(_, _ string) {}

	defer func() {
		startSSHTunnelFn = startSSHTunnel
		startSSHTunnelWithRetriesFn = startSSHTunnelWithRetries
		launchTrayAndExitFn = LaunchTrayAndExit
		startPACServerFn = startPACServer
		setSystemProxyFn = setSystemProxy
		ensureSSHAgentFn = ensureSSHAgent
		saveStateFn = SaveState
		saveLastHostFn = SaveLastHost
		startMonitoringFn = startMonitoring
		stopMonitoringFn = stopMonitoring
		killProcessByFileFn = killProcessByFile
	}()

	// Run
	isTunnelActive = false
	currentHost = ""
	host := HostConfig{Name: "testhost", HostName: "127.0.0.1"}
	handleConnectionInteractive(host)

	if !isTunnelActive {
		t.Fatal("expected isTunnelActive to be true after handleConnectionInteractive")
	}
	if currentHost != "testhost" {
		t.Fatalf("expected currentHost to be testhost, got %q", currentHost)
	}
}

func TestHandleChainConnectionInteractive_SetTunnelActive(t *testing.T) {
	cfgDir := t.TempDir()
	Config = &AppConfig{}
	Config.Network.ProxyPort = 1080
	Config.Network.PACHttpPort = 8080
	Config.Paths.WorkDir = cfgDir
	Config.TempFiles.SSHTunnelPID = cfgDir + "\\ssh_tunnel.pid"

	startSSHTunnelFn = func(_ []string) bool { return true }
	startSSHTunnelWithRetriesFn = func(_ []string, _ int) bool { return true }
	launchTrayAndExitFn = func() bool { return true }
	startPACServerFn = func() {}
	setSystemProxyFn = func(_ string) bool { return true }
	ensureSSHAgentFn = func(_, _ string) bool { return true }
	saveStateFn = func(_ ProxyState) error { return nil }
	saveLastHostFn = func(_ string) error { return nil }
	startMonitoringFn = func(_ *ProxyState) {}
	stopMonitoringFn = func() {}
	killProcessByFileFn = func(_, _ string) {}

	defer func() {
		startSSHTunnelFn = startSSHTunnel
		startSSHTunnelWithRetriesFn = startSSHTunnelWithRetries
		launchTrayAndExitFn = LaunchTrayAndExit
		startPACServerFn = startPACServer
		setSystemProxyFn = setSystemProxy
		ensureSSHAgentFn = ensureSSHAgent
		saveStateFn = SaveState
		saveLastHostFn = SaveLastHost
		startMonitoringFn = startMonitoring
		stopMonitoringFn = stopMonitoring
		killProcessByFileFn = killProcessByFile
	}()

	isTunnelActive = false
	currentHost = ""
	chain := []HostConfig{{Name: "h1", HostName: "1.1.1.1"}, {Name: "h2", HostName: "2.2.2.2"}}
	handleChainConnectionInteractive(chain)

	if !isTunnelActive {
		t.Fatal("expected isTunnelActive to be true after handleChainConnectionInteractive")
	}
	if currentHost != "h1 -> h2" {
		t.Fatalf("expected currentHost to be 'h1 -> h2', got %q", currentHost)
	}
}

func TestHandleSmartFailover_Enabled(t *testing.T) {
	cfgDir := t.TempDir()
	configPath := cfgDir + "\\ssh_config"
	_ = os.WriteFile(configPath, []byte("Host h1\n  HostName 1.1.1.1\n  User root\nHost h2\n  HostName 2.2.2.2\n  User root\n"), 0644)

	Config = &AppConfig{}
	Config.General.SmartFailover = true
	Config.Paths.WorkDir = cfgDir
	Config.Paths.SSHConfig = configPath
	Config.TempFiles.SSHTunnelPID = cfgDir + "\\ssh_tunnel.pid"
	Config.Network.ProxyPort = 1080
	Config.Network.PACHttpPort = 8080

	startSSHTunnelFn = func(_ []string) bool { return true }
	ensureSSHAgentFn = func(_, _ string) bool { return true }
	startPACServerFn = func() {}
	setSystemProxyFn = func(_ string) bool { return true }
	killProcessByFileFn = func(_, _ string) {}
	startMonitoringFn = func(_ *ProxyState) {}
	updateMenuStateFn = func() {}
	updateTrayStatusOnlineFn = func(_, _ string) {}
	checkSSHConnectionWithTimeFn = func(_ HostConfig, _ string) (bool, time.Duration) {
		return true, 1 * time.Millisecond
	}
	defer func() {
		startSSHTunnelFn = startSSHTunnel
		ensureSSHAgentFn = ensureSSHAgent
		startPACServerFn = startPACServer
		setSystemProxyFn = setSystemProxy
		killProcessByFileFn = killProcessByFile
		startMonitoringFn = startMonitoring
		updateMenuStateFn = updateMenuState
		updateTrayStatusOnlineFn = updateTrayStatusOnline
		checkSSHConnectionWithTimeFn = checkSSHConnectionWithTime
	}()

	state := &ProxyState{IsChain: false, Host: "h1", OriginalHost: "h1"}
	if !handleSmartFailover(state) {
		t.Fatal("expected handleSmartFailover to return true when available alternate hosts")
	}
}
